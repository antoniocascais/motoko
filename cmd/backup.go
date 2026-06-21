package cmd

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/antoniocascais/motoko/pkg/cloudinit"
	"github.com/antoniocascais/motoko/pkg/state"
	"github.com/antoniocascais/motoko/pkg/vm"
	"github.com/spf13/cobra"
)

var (
	backupOutputFlag string
	backupYesFlag    bool

	// Seams for testability. Each function variable delegates to the real
	// implementation; tests swap them with stubs/fakes.
	backupVMStateFn        = vm.State
	backupDumpXMLFn        = vm.DumpXML
	backupBlockDevicesFn   = vm.BlockDevices
	backupCopyDataDiskFn   = vm.CopyDataDiskQEMU
	backupCheckQCOW2Fn     = vm.CheckQCOW2
	backupGetIPFn          = vm.GetIP

	// backupRunSSHTar runs `ssh ... tar -C <dir> -czf - .` and returns the
	// stream produced by ssh. Tests substitute a fake to feed canned bytes
	// into the gzip decompression path without shelling out.
	backupRunSSHTar = defaultRunSSHTar
)

var backupCmd = &cobra.Command{
	Use:   "backup <name>",
	Short: "Snapshot an instance's disk, definition, and data",
	Long: `Capture a restorable snapshot of an instance into a local directory.

Default destination: ~/motoko-backups/<name>-<timestamp>/, where timestamp is
YYYYMMDD-HHMM in local time. The contents include the data disk, libvirt XML,
cloud-init ISO, and SSH keypair — and a ~/data tarball if the instance is
running. The directory is created mode 0700 and files mode 0600 because the ISO
holds the Telegram bot token and the keys directory holds an SSH private key.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := cloudinit.ValidateInstanceName(name); err != nil {
			return err
		}
		cfg, err := RequireConfig()
		if err != nil {
			return err
		}
		dir := configDir()
		st, err := state.Load(dir, name)
		if err != nil {
			return err
		}

		// Probing the VM state up front (instead of guessing from the
		// presence of an IP) handles the "shut off" case cleanly: no IP, no
		// tar, no warning. A shut-off instance is inherently consistent on
		// disk, so the data-disk copy is safe without a filesystem snapshot.
		vmState, err := backupVMStateFn(name)
		if err != nil {
			return fmt.Errorf("reading VM state: %w", err)
		}
		running := vmState == "running"

		if running && !backupYesFlag {
			fmt.Fprintf(os.Stderr, "WARNING: VM is running. The data-disk copy will not be crash-consistent.\n")
			if !confirmAction("Proceed with online backup?") {
				fmt.Fprintln(os.Stderr, "Aborted.")
				return nil
			}
		}

		outDir, err := resolveBackupOutputDir(backupOutputFlag, name, time.Now())
		if err != nil {
			return err
		}
		// MkdirAll's 0700 only applies to leaf directories it creates. The
		// caller might pass a --output under a pre-existing tree; we still
		// want the backup dir itself to be locked down, so Chmod after the
		// fact. Ignore ErrNotExist on Chmod in the (unlikely) race where the
		// directory disappears between MkdirAll and Chmod.
		if err := os.MkdirAll(outDir, 0700); err != nil {
			return fmt.Errorf("creating backup directory: %w", err)
		}
		if err := os.Chmod(outDir, 0700); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("locking down backup directory: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Backup target: %s\n", outDir)

		// 1. Domain XML
		fmt.Fprintln(os.Stderr, "==> Capturing domain XML")
		xml, err := backupDumpXMLFn(name)
		if err != nil {
			return err
		}
		xmlPath := filepath.Join(outDir, name+".xml")
		if err := writeSecretFile(xmlPath, []byte(xml)); err != nil {
			return err
		}

		// 2. Discover the data-disk source path from libvirt, not from naming
		// conventions. The data disk is the qcow2 in cfg.ImagesDir whose name
		// matches st.DataDiskName; the overlay and ISO are explicitly excluded.
		fmt.Fprintln(os.Stderr, "==> Discovering block devices")
		devs, err := backupBlockDevicesFn(name)
		if err != nil {
			return err
		}
		dataPath := filepath.Join(cfg.ImagesDir, st.DataDiskName)
		dataSrc, ok := findBlockDevice(devs, dataPath)
		if !ok {
			return fmt.Errorf("data disk %q not attached to VM %q (run `virsh domblklist motoko-%s` to inspect)", dataPath, name, name)
		}
		if isCDROM(dataSrc) {
			return fmt.Errorf("data disk %q resolved to a cdrom device — refusing to copy a cdrom as a disk", dataSrc.Source)
		}

		// 3. Copy + verify the data disk. -U is mandatory while the VM is
		// running (it holds the qcow2 lock); for a shut-off VM it's a no-op
		// but harmless, so we always use it.
		dataDst := filepath.Join(outDir, "data-disk.qcow2")
		fmt.Fprintf(os.Stderr, "==> Copying data disk: %s\n", dataSrc.Source)
		if err := backupCopyDataDiskFn(dataSrc.Source, dataDst); err != nil {
			return fmt.Errorf("copying data disk: %w", err)
		}
		fmt.Fprintln(os.Stderr, "==> Verifying data disk (qemu-img check)")
		if err := backupCheckQCOW2Fn(dataDst); err != nil {
			// Best-effort: remove the suspect copy so the operator doesn't
			// pick it up later thinking it's good.
			_ = os.Remove(dataDst)
			return fmt.Errorf("data disk failed qemu-img check: %w", err)
		}
		// Tighten the mode in case qemu-img created the file with a default
		// umask. The directory is 0700 already, but defence in depth — and
		// some filesystems ignore the directory mode.
		if err := os.Chmod(dataDst, 0600); err != nil {
			return fmt.Errorf("locking down data disk copy: %w", err)
		}

		// 4. Cloud-init ISO. Contains the Telegram bot token — keep it under
		// the same 0600 regime as the keys.
		fmt.Fprintln(os.Stderr, "==> Copying cloud-init ISO")
		isoBytes, err := os.ReadFile(st.CloudInitISO)
		if err != nil {
			return fmt.Errorf("reading cloud-init ISO: %w", err)
		}
		isoDst := filepath.Join(outDir, filepath.Base(st.CloudInitISO))
		if err := writeSecretFile(isoDst, isoBytes); err != nil {
			return err
		}

		// 5. SSH keypair. Source is <configDir>/keys/<name>/id_ed25519[.pub].
		fmt.Fprintln(os.Stderr, "==> Copying SSH keypair")
		if err := copyInstanceKeys(dir, name, outDir); err != nil {
			return err
		}

		// 6. ~/data tarball (running only) — the consistency backstop for
		// the non-crash-consistent disk copy. Failures here DO NOT abort the
		// backup: the disk copy is still produced and a clear warning is
		// printed so the operator knows the snapshot is degraded.
		if running {
			fmt.Fprintln(os.Stderr, "==> Pulling ~/data tarball (running instance)")
			tarPath := filepath.Join(outDir, "data.tar.gz")
			homeDir := "/home/" + cfg.VMUser
			// Resolve the IP just-in-time. The instance state may carry the
			// reserved IP but the actual lease may have moved (rebuild, etc.),
			// so re-query libvirt to be safe.
			ip, ipErr := backupGetIPFn(name)
			if ipErr != nil || ip == "" {
				fmt.Fprintf(os.Stderr, "WARNING: cannot determine VM IP for tarball (%v); skipping.\n", ipErr)
			} else if err := pullDataTarball(name, st.SSHKeyPath, cfg.VMUser, ip, homeDir, tarPath); err != nil {
				fmt.Fprintf(os.Stderr, "WARNING: data tarball failed (%v); backup is degraded but the disk copy is still on disk.\n", err)
			}
		}

		// 7. Restore-instructions README.
		if err := writeRestoreReadme(outDir, name, running); err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "\nBackup complete: %s\n", outDir)
		return nil
	},
}

func init() {
	backupCmd.Flags().StringVar(&backupOutputFlag, "output", "", "override backup destination directory (default ~/motoko-backups/<name>-<ts>/)")
	backupCmd.Flags().BoolVarP(&backupYesFlag, "yes", "y", false, "skip confirmation prompt for online backups")
	rootCmd.AddCommand(backupCmd)
}

// resolveBackupOutputDir returns the directory the backup will be written to.
// If override is non-empty it is used verbatim (after MkdirAll); otherwise the
// default is ~/motoko-backups/<name>-<timestamp>/ where timestamp is YYYYMMDD-HHMM
// in the supplied time's local zone. The function does not create the directory.
func resolveBackupOutputDir(override, name string, now time.Time) (string, error) {
	if name == "" {
		return "", fmt.Errorf("backup name is empty")
	}
	if override != "" {
		// Use the override verbatim — operator chose it, including any
		// trailing slash convention. Reject paths that try to escape
		// outside the working dir: backup names are already validated, but
		// the output flag is untrusted free-form input.
		if !filepath.IsAbs(override) {
			abs, err := filepath.Abs(override)
			if err != nil {
				return "", fmt.Errorf("resolving --output path: %w", err)
			}
			override = abs
		}
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determining home directory: %w", err)
	}
	// Local time is more useful than UTC for a human-named snapshot, and
	// DST-induced collisions are recoverable from the absolute path printed
	// by the command. We deliberately don't normalise to UTC here.
	ts := now.Format("20060102-1504")
	return filepath.Join(home, "motoko-backups", fmt.Sprintf("%s-%s", name, ts)), nil
}

// writeSecretFile writes data to path with mode 0600, creating any missing
// parent directories at 0700. Used for every backup file because they all
// contain either credentials (ISO bot token, SSH private key) or a sensitive
// VM definition (XML with MAC, RAM, memtune).
func writeSecretFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// findBlockDevice returns the device whose Source path matches want. Match is
// exact — paths from domblklist are absolute and the state-stored data path
// is built with filepath.Join, so they should always compare equal.
func findBlockDevice(devs []vm.BlockDevice, want string) (vm.BlockDevice, bool) {
	for _, d := range devs {
		if d.Source == want {
			return d, true
		}
	}
	return vm.BlockDevice{}, false
}

// isCDROM reports whether a block device is a cdrom, by inspecting the Source
// extension. cloud-init ISOs are .iso; data disks are .qcow2. We don't have a
// device type in the parsed domblklist output, so this is the cheapest signal.
func isCDROM(d vm.BlockDevice) bool {
	return strings.HasSuffix(strings.ToLower(d.Source), ".iso")
}

// copyInstanceKeys copies <configDir>/keys/<name>/id_ed25519[.pub] into
// <outDir>/keys/, both at 0600. The .pub file is technically world-readable,
// but a stricter mode is harmless and keeps the directory mode-uniform.
func copyInstanceKeys(configDir, name, outDir string) error {
	srcDir := filepath.Join(configDir, "keys", name)
	for _, fname := range []string{"id_ed25519", "id_ed25519.pub"} {
		srcPath := filepath.Join(srcDir, fname)
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("reading SSH key %s: %w", srcPath, err)
		}
		dstPath := filepath.Join(outDir, "keys", fname)
		if err := writeSecretFile(dstPath, data); err != nil {
			return err
		}
	}
	return nil
}

// pullDataTarball runs `ssh ... tar -C <remoteHome>/data -czf - .` and writes
// the gzipped tarball to localPath. The IP is supplied by the caller
// (resolved just-in-time from libvirt) so this function does not need to call
// back into the VM package. The remote tar invocation is a constant string —
// no user input is interpolated into a shell, and the tar args are fixed.
//
// The seam `backupRunSSHTar` returns an io.ReadCloser producing the remote
// tar's stdout; the default implementation shells out to ssh. Tests inject a
// fake that returns a bytes.Reader.
func pullDataTarball(name, keyPath, user, ip, remoteHome, localPath string) error {
	dir := configDir()
	knownHosts := state.KnownHostsPath(dir, name)
	sshArgv := append(sshArgs(keyPath, user, ip, knownHosts),
		"tar", "-C", remoteHome+"/data", "-czf", "-", ".",
	)
	rc, err := backupRunSSHTar(sshArgv)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()

	f, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	// Stream the remote gzipped tar to disk, then validate the resulting
	// file with a gzip reader. Validating the bytes-on-disk (rather than
	// pipe through gzip on the way in) avoids the awkward "consume the
	// stream twice" pattern, and matches the actual file the operator
	// will use for restore.
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing tarball to %s: %w", localPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing tarball: %w", err)
	}
	if err := validateGzipFile(localPath); err != nil {
		return err
	}
	if err := os.Chmod(localPath, 0600); err != nil {
		return fmt.Errorf("locking down tarball: %w", err)
	}
	return nil
}

// validateGzipFile opens path, reads it through a gzip.Reader, and returns an
// error if the file isn't valid gzip or is empty. A corrupt tarball is
// detected here rather than at restore time so the operator sees the problem
// immediately and can re-run the backup while the source VM is still
// available.
func validateGzipFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s is not valid gzip: %w", path, err)
	}
	defer func() { _ = gz.Close() }()
	n, err := io.Copy(io.Discard, gz)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if n == 0 {
		return fmt.Errorf("%s is empty", path)
	}
	return nil
}

// defaultRunSSHTar runs ssh with the given argv and returns a ReadCloser of
// its stdout. Closing the ReadCloser waits on the ssh process (or kills it
// if the caller abandoned the stream) so we never leak a child.
func defaultRunSSHTar(sshArgv []string) (io.ReadCloser, error) {
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("ssh not found in PATH: %w", err)
	}
	cmd := exec.Command(sshBin, sshArgv...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("capturing ssh stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting ssh: %w", err)
	}
	return &cmdReader{Reader: stdout, cmd: cmd}, nil
}

// cmdReader wraps an io.Reader with a process handle. Close kills the process
// (if still running) and waits for it. Used by defaultRunSSHTar to keep ssh
// from outliving its parent if the caller bails out mid-stream.
type cmdReader struct {
	io.Reader
	cmd    *exec.Cmd
	closed bool
}

func (r *cmdReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.cmd.ProcessState == nil {
		// Process still running — kill it and wait for it to exit. If the
		// caller already called Wait themselves, ProcessState is non-nil
		// and we skip straight to returning nil; calling Wait twice on the
		// same exec.Cmd returns "Wait was already called".
		_ = r.cmd.Process.Kill()
		return r.cmd.Wait()
	}
	return nil
}

// writeRestoreReadme drops a human-readable README.txt into the backup dir
// describing the contents and the manual restore procedure (per ticket: no
// restore command, just documentation).
func writeRestoreReadme(outDir, name string, hadDataTarball bool) error {
	var b strings.Builder
	fmt.Fprintf(&b, "motoko backup of %q\n", name)
	fmt.Fprintf(&b, "Created: %s\n\n", time.Now().Format(time.RFC3339))

	b.WriteString("Contents\n")
	b.WriteString("--------\n")
	if hadDataTarball {
		b.WriteString("  data.tar.gz         ~/data tarball (filesystem-level, running backup)\n")
	}
	b.WriteString("  data-disk.qcow2     Data disk (block-level, qcow2)\n")
	b.WriteString("  <name>.xml          virsh dumpxml output (VM definition)\n")
	b.WriteString("  <name>-cloud-init.iso  cloud-init seed (contains bot token — keep secret)\n")
	b.WriteString("  keys/               SSH keypair for the instance\n")
	b.WriteString("  README.txt          This file\n\n")

	b.WriteString("Notes\n")
	b.WriteString("-----\n")
	if hadDataTarball {
		b.WriteString("The data-disk copy is NOT crash-consistent. Treat data.tar.gz as the\n")
		b.WriteString("authoritative filesystem view; the disk copy is the block-level complement\n")
		b.WriteString("that preserves partition layout, mountpoints, and file permissions.\n\n")
	}

	b.WriteString("Restore (file-level)\n")
	b.WriteString("--------------------\n")
	b.WriteString("1. Create a fresh instance with the same name (or one with the same data\n")
	b.WriteString("   disk attached).\n")
	b.WriteString("2. SSH into it: `motoko ssh <name>`.\n")
	b.WriteString("3. Extract the tarball into the data directory:\n")
	b.WriteString("     rm -rf ~/data && mkdir ~/data && tar -xzf data.tar.gz -C ~/data\n\n")

	b.WriteString("Restore (block-level)\n")
	b.WriteString("---------------------\n")
	b.WriteString("1. Stop the target instance: `motoko stop <name>`.\n")
	b.WriteString("2. Replace its data disk with the backup copy:\n")
	b.WriteString("     sudo cp data-disk.qcow2 <libvirt images dir>/motoko-<name>-data.qcow2\n")
	b.WriteString("3. Start the instance: `motoko start <name>`.\n\n")

	b.WriteString("All files in this directory are mode 0600 (dir 0700) because the ISO\n")
	b.WriteString("holds the Telegram bot token and keys/ holds the SSH private key.\n")
	return writeSecretFile(filepath.Join(outDir, "README.txt"), []byte(b.String()))
}
