package release

import (
	"context"
	"fmt"
	"sync"

	"github.com/zapstore/zsp/v2/internal/apk"
	"github.com/zapstore/zsp/v2/internal/source"
)

// probeMinSize is the size under which an asset is downloaded outright: a few
// range requests cost about as much as the file. A variable so tests can move
// it.
var probeMinSize int64 = 2 << 20

const (
	// probeBudget is the most one asset's identity may cost. Measured over real
	// APKs it takes 0.14 to 0.87 MB.
	probeBudget int64 = 4 << 20
	// probeWorkers bounds the range reads in flight.
	probeWorkers = 4
)

// downloadPlan decides which candidates Fetch downloads, and in what order.
//
// It reads each candidate's identity with a few range requests first. A
// candidate that cannot be chosen whatever else turns up is never downloaded;
// one whose identity could not be read is always downloaded, as it would be
// without probing. A probed candidate is only a prediction: when a download
// fails verification the next best one in its group is tried.
type downloadPlan struct {
	order   []*source.Asset
	free    []*source.Asset
	groups  []*probeGroup
	groupOf map[*source.Asset]*probeGroup
}

// probeGroup is the probed candidates of one app, which is one package and
// one certificate, as selectFetchedAPKs groups them.
type probeGroup struct {
	members  []probeMember
	tried    map[*source.Asset]bool
	verified bool
}

type probeMember struct {
	asset *source.Asset
	id    *apk.Identity
}

// planDownloads probes candidates when that can save a download: the source
// can read ranges and more than one candidate is worth reading. Otherwise
// every candidate is downloaded, in order.
func planDownloads(ctx context.Context, src source.Source, candidates []*source.Asset, options FetchOptions) *downloadPlan {
	plan := &downloadPlan{order: candidates, groupOf: make(map[*source.Asset]*probeGroup)}
	opener, ok := src.(source.RangeOpener)
	if !ok || len(candidates) < 2 {
		plan.free = candidates
		return plan
	}
	worth := 0
	for _, candidate := range candidates {
		if worthProbing(candidate) {
			worth++
		}
	}
	if worth < 2 {
		// One probe cannot rule out a download that is needed anyway.
		plan.free = candidates
		return plan
	}

	report(options, "fetch", "inspect", fmt.Sprintf("%d APK candidates", len(candidates)), 0, 0)
	ids := make([]*apk.Identity, len(candidates))
	var wg sync.WaitGroup
	slots := make(chan struct{}, probeWorkers)
	for i, candidate := range candidates {
		if !worthProbing(candidate) {
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			ids[i] = probeIdentity(ctx, opener, candidate)
		}()
	}
	wg.Wait()

	byApp := make(map[string]*probeGroup)
	for i, candidate := range candidates {
		id := ids[i]
		if id == nil {
			plan.free = append(plan.free, candidate)
			continue
		}
		key := id.PackageID + "\x00" + id.CertFingerprint
		group := byApp[key]
		if group == nil {
			group = &probeGroup{tried: make(map[*source.Asset]bool)}
			byApp[key] = group
			plan.groups = append(plan.groups, group)
		}
		group.members = append(group.members, probeMember{asset: candidate, id: id})
		plan.groupOf[candidate] = group
	}
	return plan
}

// worthProbing reports whether an asset can be read in ranges and is big
// enough for that to pay.
func worthProbing(asset *source.Asset) bool {
	if asset.LocalPath != "" || asset.URL == "" {
		return false
	}
	return asset.Size == 0 || asset.Size >= probeMinSize
}

// probeIdentity returns the identity of an asset read in ranges, or nil when it
// could not be read. Nil is not a verdict on the asset: the caller downloads it.
func probeIdentity(ctx context.Context, opener source.RangeOpener, asset *source.Asset) *apk.Identity {
	reader, err := opener.OpenRange(ctx, asset, probeBudget)
	if err != nil {
		return nil
	}
	id, err := apk.ReadIdentity(ctx, reader, reader.Size())
	// A parser may take a failed read for a missing structure, so what the
	// reader saw outranks what the parser concluded.
	if err != nil || reader.Err() != nil {
		return nil
	}
	return id
}

// first returns the candidates to download first, in candidate order: those
// that could not be probed, and the best of each probed app.
func (p *downloadPlan) first() []*source.Asset {
	pick := make(map[*source.Asset]bool, len(p.order))
	for _, asset := range p.free {
		pick[asset] = true
	}
	return p.add(pick, p.groups)
}

// retry returns the candidates to download after a round, in candidate order:
// the best remaining ones of each probed app for which nothing has passed
// verification. It is empty when every such app has a verified candidate or
// has none left.
func (p *downloadPlan) retry() []*source.Asset {
	return p.add(make(map[*source.Asset]bool), p.groups)
}

func (p *downloadPlan) add(pick map[*source.Asset]bool, groups []*probeGroup) []*source.Asset {
	for _, group := range groups {
		if group.verified {
			continue
		}
		for _, asset := range group.next() {
			pick[asset] = true
		}
	}
	picked := make([]*source.Asset, 0, len(pick))
	for _, asset := range p.order {
		if pick[asset] {
			picked = append(picked, asset)
		}
	}
	return picked
}

// verified records that a candidate passed verification, which ends the
// search in its app.
func (p *downloadPlan) verified(asset *source.Asset) {
	if group := p.groupOf[asset]; group != nil {
		group.verified = true
	}
}

// next returns the untried members that no other untried member beats, and
// marks them tried.
func (g *probeGroup) next() []*source.Asset {
	var remaining []probeMember
	for _, member := range g.members {
		if !g.tried[member.asset] {
			remaining = append(remaining, member)
		}
	}
	var picked []*source.Asset
	for _, member := range remaining {
		if !dominated(member, remaining) {
			picked = append(picked, member.asset)
		}
	}
	if len(picked) == 0 {
		// The rules cannot rank every member below another; this is a guard.
		for _, member := range remaining {
			picked = append(picked, member.asset)
		}
	}
	for _, asset := range picked {
		g.tried[asset] = true
	}
	return picked
}

// dominated reports whether selectFetchedAPKs would never choose x while any
// of among is present, however the details a probe cannot see turn out. Each
// rule needs a rival that declares arm64-v8a, which the probe sees exactly.
// An uncertain x, with no ABI named, is never dominated by a rival it might
// outrank.
//
//   - A rival of a greater version_code wins.
//   - An x that names ABIs but not arm64-v8a is not in the pool.
//   - At one version_code an arm64-only rival wins over a fat x.
func dominated(x probeMember, among []probeMember) bool {
	for _, y := range among {
		if y.asset == x.asset || !declaresArm64(y.id.Architectures) {
			continue
		}
		xArchs := x.id.Architectures
		switch {
		case y.id.VersionCode > x.id.VersionCode:
			return true
		case len(xArchs) > 0 && !declaresArm64(xArchs):
			return true
		case y.id.VersionCode == x.id.VersionCode &&
			onlyArm64(y.id.Architectures) && declaresArm64(xArchs) && !onlyArm64(xArchs):
			return true
		}
	}
	return false
}

func declaresArm64(architectures []string) bool {
	for _, arch := range architectures {
		if arch == "arm64-v8a" {
			return true
		}
	}
	return false
}
