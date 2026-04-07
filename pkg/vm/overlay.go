package vm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CreateOverlay creates a qcow2 overlay backed by the golden image.
// The backing path must be absolute (qemu-img requirement).
func CreateOverlay(imagesDir, goldenName, overlayName string) error {
	goldenPath := filepath.Join(imagesDir, goldenName)
	if !filepath.IsAbs(goldenPath) {
		return fmt.Errorf("golden image backing path must be absolute: %s", goldenPath)
	}

	overlayPath := filepath.Join(imagesDir, overlayName)
	_, _, err := runCmd("qemu-img", "create", "-f", "qcow2", "-b", goldenPath, "-F", "qcow2", overlayPath)
	return err
}

// CreateDataDisk creates a new qcow2 data disk of the given size.
func CreateDataDisk(imagesDir, dataName string, sizeGB int) error {
	dataPath := filepath.Join(imagesDir, dataName)
	_, _, err := runCmd("qemu-img", "create", "-f", "qcow2", dataPath, fmt.Sprintf("%dG", sizeGB))
	return err
}

// ResetOverlay deletes the existing overlay and recreates it from the golden image.
func ResetOverlay(imagesDir, goldenName, overlayName string) error {
	overlayPath := filepath.Join(imagesDir, overlayName)
	if err := os.Remove(overlayPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing overlay: %w", err)
	}
	return CreateOverlay(imagesDir, goldenName, overlayName)
}
