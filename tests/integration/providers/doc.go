//go:build providers

// Package providers fetches live releases and checks them against the public
// Zapstore relay. Forge parsing stays in internal/source with httptest stubs.
//
//	make test
package providers
