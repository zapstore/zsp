package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/zapstore/zsp/v2"
	"github.com/zapstore/zsp/v2/internal/apk"
	"github.com/zapstore/zsp/v2/internal/cli"
)

type apkMetadataDocument struct {
	PackageID       string   `json:"package_id"`
	VersionName     string   `json:"version_name"`
	VersionCode     int64    `json:"version_code"`
	MinSDK          int32    `json:"min_sdk"`
	TargetSDK       int32    `json:"target_sdk"`
	Label           string   `json:"label"`
	Architectures   []string `json:"architectures"`
	CertificateHash string   `json:"certificate_hash"`
	Hash            string   `json:"hash"`
	Size            int64    `json:"size"`
	Icon            string   `json:"icon,omitempty"`
}

func utilsCommand(ctx context.Context, opts *cli.Options) int {
	switch opts.Utils.Operation {
	case "extract-apk":
		return extractAPKCommand(ctx, opts)
	case "find-unsigned":
		return findUnsignedCommand(opts)
	default:
		return writePublishError(opts, &cliOperationError{code: "invalid_arguments", message: "unknown utils operation", cause: zsp.ErrInvalidConfig}, nil)
	}
}

func extractAPKCommand(ctx context.Context, opts *cli.Options) int {
	info, err := apk.Parse(ctx, opts.Args[0])
	if err != nil {
		return writePublishError(opts, &cliOperationError{code: "invalid_apk", message: err.Error(), cause: zsp.ErrInvalidAPK}, nil)
	}
	doc := apkMetadataDocument{
		PackageID: info.PackageID, VersionName: info.VersionName, VersionCode: info.VersionCode,
		MinSDK: info.MinSDK, TargetSDK: info.TargetSDK, Label: info.Label,
		Architectures: info.Architectures, CertificateHash: info.CertFingerprint,
		Hash: info.SHA256, Size: info.FileSize,
	}
	if doc.Architectures == nil {
		doc.Architectures = []string{}
	}
	if opts.Utils.IconPath != "" {
		if len(info.Icon) > 0 {
			if err := os.WriteFile(opts.Utils.IconPath, info.Icon, 0o644); err != nil {
				return writePublishError(opts, &cliOperationError{code: "invalid_arguments", message: err.Error(), cause: zsp.ErrInvalidConfig}, nil)
			}
		}
	} else if len(info.Icon) > 0 {
		doc.Icon = base64.StdEncoding.EncodeToString(info.Icon)
	}
	encoder := json.NewEncoder(os.Stdout)
	if opts.Global.JSON {
		return encodeResult(encoder.Encode(struct {
			OK        bool                `json:"ok"`
			Operation string              `json:"operation"`
			APK       apkMetadataDocument `json:"apk"`
		}{true, "extract_apk", doc}))
	}
	return encodeResult(encoder.Encode(doc))
}

func encodeResult(err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func findUnsignedCommand(opts *cli.Options) int {
	dir := opts.Args[0]
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		message := dir + " is not a directory"
		if err != nil {
			message = err.Error()
		}
		return writePublishError(opts, &cliOperationError{code: "invalid_arguments", message: message, cause: zsp.ErrInvalidConfig}, nil)
	}
	paths, err := zsp.FindUnsigned(dir)
	if err != nil {
		return writePublishError(opts, &cliOperationError{code: "invalid_apk", message: err.Error(), cause: zsp.ErrInvalidAPK}, nil)
	}
	if len(paths) > 0 {
		fmt.Fprintln(os.Stdout, strings.Join(paths, "\n"))
	}
	return 0
}
