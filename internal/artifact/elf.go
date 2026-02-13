package artifact

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// ELFParser extracts metadata from ELF (Executable and Linkable Format) binaries.
// These are native Linux executables. Per NIP-82, they should be statically linked.
type ELFParser struct{}

// Parse extracts metadata from an ELF binary.
// Determines architecture, whether the binary is statically linked, and file metadata.
// Identifier, Version, and Name are left empty for the caller to set.
func (p *ELFParser) Parse(path string) (*AssetInfo, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open ELF file: %w", err)
	}
	defer f.Close()

	// Validate this is an executable (not a shared library or object file).
	if f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN {
		return nil, fmt.Errorf("ELF file is not an executable (type: %s)", f.Type)
	}

	// For ET_DYN, distinguish PIE executables from shared libraries.
	// PIE executables have an entry point; pure shared libs typically don't,
	// but this is not a reliable heuristic. We allow ET_DYN since Go and
	// most modern compilers produce PIE by default.

	// Determine architecture.
	arch := elfMachineToArch(f.Machine, f.Class)
	if arch == "" {
		return nil, fmt.Errorf("unsupported ELF architecture: %s", f.Machine)
	}

	platform := "linux-" + arch

	// Check if statically linked (no PT_INTERP program header).
	isStatic := true
	for _, prog := range f.Progs {
		if prog.Type == elf.PT_INTERP {
			isStatic = false
			break
		}
	}

	// Warn about dynamically linked binaries — NIP-82 recommends static linking.
	if !isStatic {
		// Not an error, but callers may want to surface this.
		// We proceed and let the publisher decide.
	}

	// Compute file hash and size.
	sha256Hash, fileSize, err := hashAndSize(path)
	if err != nil {
		return nil, fmt.Errorf("failed to hash ELF file: %w", err)
	}

	return &AssetInfo{
		FilePath:  path,
		FileSize:  fileSize,
		SHA256:    sha256Hash,
		MIMEType:  MIMELinuxExecutable,
		Platforms: []string{platform},
	}, nil
}

// elfMachineToArch maps ELF machine types to NIP-82 architecture identifiers.
// Linux uses "aarch64" for ARM64 (matching `uname -m`).
func elfMachineToArch(machine elf.Machine, class elf.Class) string {
	switch machine {
	case elf.EM_X86_64:
		return "x86_64"
	case elf.EM_AARCH64:
		return "aarch64"
	case elf.EM_386:
		return "x86"
	case elf.EM_ARM:
		return "armv7l"
	case elf.EM_RISCV:
		if class == elf.ELFCLASS64 {
			return "riscv64"
		}
		return "" // 32-bit RISC-V not in NIP-82
	default:
		return ""
	}
}

// hashAndSize computes the SHA-256 hash and size of a file.
func hashAndSize(path string) (hash string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return "", 0, err
	}

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, err
	}

	return hex.EncodeToString(h.Sum(nil)), fi.Size(), nil
}
