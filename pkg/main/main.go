package main

import (
	"fmt"
	"os"
	"syscall"

	"sigs.k8s.io/dra-example-driver/pkg/amdgpu"
)

// getDeviceAttrs gets the major, minor, type, and permissions for a given device path.
func getDeviceAttrs(path string) (major, minor int64, devType, permissions string, err error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return 0, 0, "", "", fmt.Errorf("failed to stat device %s: %w", path, err)
	}

	// fileInfo.Sys() returns the underlying data source of the FileInfo.
	// For Linux, it's a *syscall.Stat_t.
	stat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, "", "", fmt.Errorf("failed to get syscall.Stat_t for %s", path)
	}

	// st_rdev contains the encoded major and minor numbers for device special files.
	// The encoding is OS-specific (Linux in this case).
	// On Linux, the standard way to extract major/minor from dev_t (st_rdev) is:
	// Major = (st_rdev >> 8) & 0xff
	// Minor = (st_rdev & 0xff) | ((st_rdev >> 12) & 0xfff00)
	// However, Go's x/sys/unix provides helper functions, or you can use the more common C-style macros.
	// The current Linux kernel defines MAJOR/MINOR as:
	// #define MINORBITS       20
	// #define MINORMASK       ((1U << MINORBITS) - 1)
	// #define MAJOR(dev)      ((unsigned int) ((dev) >> MINORBITS))
	// #define MINOR(dev)      ((unsigned int) ((dev) & MINORMASK))
	// Golang's x/sys/unix also implements these:
	// func Major(dev uint64) uint32
	// func Minor(dev uint64) uint32
	// We'll use the syscall.Stat_t.Rdev and perform the bitwise operations directly for simplicity here.
	// If you are only targeting Linux, this is reliable.

	major = int64(stat.Rdev >> 8 & 0xff)
	minor = int64(stat.Rdev & 0xff)
	major |= int64(stat.Rdev>>32) & 0xfffff000
	minor |= int64(stat.Rdev>>12) & 0xffffff00

	// Determine device type
	if (fileInfo.Mode() & os.ModeCharDevice) != 0 {
		devType = "c"
	} else if (fileInfo.Mode() & os.ModeDevice) != 0 {
		devType = "b" // Block device
	} else {
		return 0, 0, "", "", fmt.Errorf("unsupported file type for device %s: %v", path, fileInfo.Mode())
	}

	// Determine permissions (simplified, "rwm" is common for devices)
	// You could make this more granular if needed, based on fileInfo.Mode().Perm()
	permissions = "rwm" // Default to read, write, mknod

	return major, minor, devType, permissions, nil
}

func main() {
	fmt.Println(amdgpu.GetAMDGPUs())
	// Example usage of the AMDGPU package
	fmt.Println(getDeviceAttrs("/dev/dri/card1"))
	fmt.Println(amdgpu.OpenAMDGPU("card1"))
}
