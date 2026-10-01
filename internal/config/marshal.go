package config

// MarshalYAML renders the configuration in the canonical zapstore.yaml shape so
// it can be stored and read back with Parse.
func (c Config) MarshalYAML() (any, error) {
	result := yamlConfig{
		Repository:            c.Repository,
		ReleaseSource:         c.ReleaseSource,
		ReleaseFilter:         c.ReleaseFilter,
		Match:                 c.Match,
		PrereleaseChannel:     c.PrereleaseChannel,
		Channel:               c.Channel,
		Name:                  c.Name,
		Description:           c.Description,
		Summary:               c.Summary,
		Tags:                  c.Tags,
		License:               c.License,
		Website:               c.Website,
		Icon:                  c.Icon,
		Images:                c.Images,
		ReleaseNotes:          c.ReleaseNotes,
		SupportedNIPs:         c.SupportedNIPs,
		MinAllowedVersion:     c.MinAllowedVersion,
		MinAllowedVersionCode: c.MinAllowedVersionCode,
	}
	// A nil MetadataSources means automatic selection while a non-nil empty
	// slice disables metadata fetching; keep the distinction across round trips.
	if c.MetadataSources != nil {
		sources := c.MetadataSources
		result.MetadataSources = &sources
	}
	return result, nil
}

// MarshalYAML renders a release source as a bare string when it carries no
// structured settings, matching the shorthand Parse accepts, and as a mapping
// otherwise.
func (r *ReleaseSource) MarshalYAML() (any, error) {
	if r == nil {
		return nil, nil
	}
	if r.Type == "" && r.AssetURL == "" && r.Version == nil && r.Asset == nil {
		switch {
		case r.LocalPath != "":
			return r.LocalPath, nil
		case r.URL != "":
			return r.URL, nil
		}
	}
	url := r.URL
	if url == "" {
		url = r.LocalPath
	}
	return webReleaseSource{URL: url, Type: r.Type, AssetURL: r.AssetURL, Version: r.Version, Asset: r.Asset}, nil
}

// yamlConfig mirrors the canonical zapstore.yaml key layout for marshaling.
type yamlConfig struct {
	Repository            string         `yaml:"repository,omitempty"`
	ReleaseSource         *ReleaseSource `yaml:"release_source,omitempty"`
	ReleaseFilter         string         `yaml:"release_filter,omitempty"`
	Match                 string         `yaml:"match,omitempty"`
	PrereleaseChannel     string         `yaml:"prerelease_channel,omitempty"`
	Channel               string         `yaml:"channel,omitempty"`
	Name                  string         `yaml:"name,omitempty"`
	Description           string         `yaml:"description,omitempty"`
	Summary               string         `yaml:"summary,omitempty"`
	Tags                  []string       `yaml:"tags,omitempty"`
	License               string         `yaml:"license,omitempty"`
	Website               string         `yaml:"website,omitempty"`
	Icon                  string         `yaml:"icon,omitempty"`
	Images                []string       `yaml:"images,omitempty"`
	ReleaseNotes          string         `yaml:"release_notes,omitempty"`
	SupportedNIPs         []string       `yaml:"supported_nips,omitempty"`
	MinAllowedVersion     string         `yaml:"min_allowed_version,omitempty"`
	MinAllowedVersionCode int64          `yaml:"min_allowed_version_code,omitempty"`
	MetadataSources       *[]string      `yaml:"metadata_sources,omitempty"`
}
