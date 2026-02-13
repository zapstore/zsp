package artifact

import (
	"debug/macho"
	"fmt"
)

// MachOParser extracts metadata from Mach-O binaries.
// These are native macOS executables. Supports both single-architecture
// and universal (fat) binaries.
type MachOParser struct{}

// Parse extracts metadata from a Mach-O binary.
// For universal (fat) binaries, all contained architectures are reported
// as separate platform identifiers. Identifier, Version, and Name are
// left empty for the caller to set.
func (p *MachOParser) Parse(path string) (*AssetInfo, error) {
	// Try as a universal (fat) binary first.
	if info, err := p.parseFat(path); err == nil {
		return info, nil
	}

	// Fall back to single-architecture binary.
	return p.parseSingle(path)
}

// parseSingle parses a single-architecture Mach-O binary.
func (p *MachOParser) parseSingle(path string) (*AssetInfo, error) {
	f, err := macho.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open Mach-O file: %w", err)
	}
	defer f.Close()

	// Validate this is an executable.
	if f.Type != macho.TypeExec {
		return nil, fmt.Errorf("Mach-O file is not an executable (type: %d)", f.Type)
	}

	arch := machoCPUToArch(f.Cpu)
	if arch == "" {
		return nil, fmt.Errorf("unsupported Mach-O architecture: %s", f.Cpu)
	}

	platform := "darwin-" + arch

	sha256Hash, fileSize, err := hashAndSize(path)
	if err != nil {
		return nil, fmt.Errorf("failed to hash Mach-O file: %w", err)
	}

	return &AssetInfo{
		FilePath:  path,
		FileSize:  fileSize,
		SHA256:    sha256Hash,
		MIMEType:  MIMEMachOBinary,
		Platforms: []string{platform},
	}, nil
}

// parseFat parses a universal (fat) Mach-O binary containing multiple architectures.
func (p *MachOParser) parseFat(path string) (*AssetInfo, error) {
	f, err := macho.OpenFat(path)
	if err != nil {
		return nil, fmt.Errorf("not a fat Mach-O: %w", err)
	}
	defer f.Close()

	var platforms []string
	for _, arch := range f.Arches {
		// Only include executable slices.
		if arch.Type != macho.TypeExec {
			continue
		}

		archName := machoCPUToArch(arch.Cpu)
		if archName != "" {
			platforms = append(platforms, "darwin-"+archName)
		}
	}

	if len(platforms) == 0 {
		return nil, fmt.Errorf("fat Mach-O contains no supported executable architectures")
	}

	sha256Hash, fileSize, err := hashAndSize(path)
	if err != nil {
		return nil, fmt.Errorf("failed to hash Mach-O file: %w", err)
	}

	return &AssetInfo{
		FilePath:  path,
		FileSize:  fileSize,
		SHA256:    sha256Hash,
		MIMEType:  MIMEMachOBinary,
		Platforms: platforms,
	}, nil
}

// machoCPUToArch maps Mach-O CPU types to NIP-82 architecture identifiers.
// macOS uses "arm64" (matching `uname -m` on Apple Silicon).
func machoCPUToArch(cpu macho.Cpu) string {
	switch cpu {
	case macho.CpuAmd64:
		return "x86_64"
	case macho.CpuArm64:
		return "arm64"
	default:
		return ""
	}
}
