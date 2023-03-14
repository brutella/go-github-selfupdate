package selfupdate

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"

	"github.com/blang/semver"
	"github.com/google/go-github/v30/github"
)

var reVersion = regexp.MustCompile(`\d+\.\d+\.\d+`)

type options struct {
	draft bool
	pre   bool
}

func findAssetFromRelease(rel *github.RepositoryRelease,
	suffixes []string, targetVersion string, filters []*regexp.Regexp, opt options) (*github.ReleaseAsset, semver.Version, bool) {

	if targetVersion != "" && targetVersion != rel.GetTagName() {
		log.Println("Skip", rel.GetTagName(), "not matching to specified version", targetVersion)
		return nil, semver.Version{}, false
	}

	if targetVersion == "" && rel.GetDraft() && !opt.draft {
		log.Println("Skip draft version", rel.GetTagName())
		return nil, semver.Version{}, false
	}
	if targetVersion == "" && rel.GetPrerelease() && !opt.pre {
		log.Println("Skip pre-release version", rel.GetTagName())
		return nil, semver.Version{}, false
	}

	verText := rel.GetTagName()
	indices := reVersion.FindStringIndex(verText)
	if indices == nil {
		log.Println("Skip version not adopting semver", verText)
		return nil, semver.Version{}, false
	}
	if indices[0] > 0 {
		log.Println("Strip prefix of version", verText[:indices[0]], "from", verText)
		verText = verText[indices[0]:]
	}

	// If semver cannot parse the version text, it means that the text is not adopting
	// the semantic versioning. So it should be skipped.
	ver, err := semver.Make(verText)
	if err != nil {
		log.Println("Failed to parse a semantic version", verText)
		return nil, semver.Version{}, false
	}

	for _, asset := range rel.Assets {
		name := asset.GetName()
		if len(filters) > 0 {
			// if some filters are defined, match them: if any one matches, the asset is selected
			matched := false
			for _, filter := range filters {
				if filter.MatchString(name) {
					log.Println("Selected filtered asset", name)
					matched = true
					break
				}
				log.Printf("Skipping asset %q not matching filter %v\n", name, filter)
			}
			if !matched {
				continue
			}
		}

		for _, s := range suffixes {
			if strings.HasSuffix(name, s) { // require version, arch etc
				// default: assume single artifact
				return asset, ver, true
			}
		}
	}

	log.Println("No suitable asset was found in release", rel.GetTagName())
	return nil, semver.Version{}, false
}

func findValidationAsset(rel *github.RepositoryRelease, validationName string) (*github.ReleaseAsset, bool) {
	for _, asset := range rel.Assets {
		if asset.GetName() == validationName {
			return asset, true
		}
	}
	return nil, false
}

func (up *Updater) findAsset(rel *github.RepositoryRelease) *github.ReleaseAsset {
	// Generate candidates
	suffixes := make([]string, 0, 2*7*2)
	for _, sep := range []rune{'_', '-'} {
		for _, ext := range []string{".zip", ".tar.gz", ".tgz", ".gzip", ".gz", ".tar.xz", ".xz", ""} {
			suffix := fmt.Sprintf("%s%c%s%s", runtime.GOOS, sep, runtime.GOARCH, ext)
			suffixes = append(suffixes, suffix)
			if runtime.GOOS == "windows" {
				suffix = fmt.Sprintf("%s%c%s.exe%s", runtime.GOOS, sep, runtime.GOARCH, ext)
				suffixes = append(suffixes, suffix)
			}
		}
	}

	asset, _, _ := findAssetFromRelease(rel, suffixes, "", up.filters, options{pre: up.pre, draft: up.draft})
	return asset
}

func findReleaseAndAsset(rels []*github.RepositoryRelease, targetVersion string, filters []*regexp.Regexp, opt options) (*github.RepositoryRelease, *github.ReleaseAsset, semver.Version, bool) {
	// Generate candidates
	suffixes := make([]string, 0, 2*7*2)
	for _, sep := range []rune{'_', '-'} {
		for _, ext := range []string{".zip", ".tar.gz", ".tgz", ".gzip", ".gz", ".tar.xz", ".xz", ""} {
			suffix := fmt.Sprintf("%s%c%s%s", runtime.GOOS, sep, runtime.GOARCH, ext)
			suffixes = append(suffixes, suffix)
			if runtime.GOOS == "windows" {
				suffix = fmt.Sprintf("%s%c%s.exe%s", runtime.GOOS, sep, runtime.GOARCH, ext)
				suffixes = append(suffixes, suffix)
			}
		}
	}

	var ver semver.Version
	var asset *github.ReleaseAsset
	var release *github.RepositoryRelease

	// Find the latest version from the list of releases.
	// Returned list from GitHub API is in the order of the date when created.
	//   ref: https://github.com/rhysd/go-github-selfupdate/issues/11
	for _, rel := range rels {
		if a, v, ok := findAssetFromRelease(rel, suffixes, targetVersion, filters, opt); ok {
			// Note: any version with suffix is less than any version without suffix.
			// e.g. 0.0.1 > 0.0.1-beta
			if release == nil || v.GTE(ver) {
				ver = v
				asset = a
				release = rel
			}
		}
	}

	if release == nil {
		log.Println("Could not find any release for", runtime.GOOS, "and", runtime.GOARCH)
		return nil, nil, semver.Version{}, false
	}

	return release, asset, ver, true
}

// DetectLatest tries to get the latest version of the repository on GitHub. 'slug' means 'owner/name' formatted string.
// It fetches releases information from GitHub API and find out the latest release with matching the tag names and asset names.
// Drafts and pre-releases are ignored. Assets would be suffixed by the OS name and the arch name such as 'foo_linux_amd64'
// where 'foo' is a command name. '-' can also be used as a separator. File can be compressed with zip, gzip, zxip, tar&zip or tar&zxip.
// So the asset can have a file extension for the corresponding compression format such as '.zip'.
// On Windows, '.exe' also can be contained such as 'foo_windows_amd64.exe.zip'.
func (up *Updater) DetectLatest(slug string) (release *Release, found bool, err error) {
	return up.DetectVersion(slug, "")
}

func (up *Updater) fetchReleases(owner, name string) ([]*github.RepositoryRelease, error) {
	rels, res, err := up.api.Repositories.ListReleases(up.apiCtx, owner, name, nil)
	if err != nil {
		log.Println("API returned an error response:", err)
		if res != nil && res.StatusCode == 404 {
			// 404 means repository not found or release not found. It's not an error here.
			err = nil
			log.Println("API returned 404. Repository or release not found")
		}
		return nil, err
	}
	return rels, nil
}

// ListReleases returns a list of releases of the repository on GitHub. Only releases with an asset of the current
// platform are included.
func (up *Updater) ListReleases(slug string) ([]*Release, error) {
	owner, name, err := parseSlug(slug)
	if err != nil {
		return nil, err
	}

	rels, err := up.fetchReleases(owner, name)
	if err != nil {
		return nil, err
	}

	var tmp []*Release
	for _, rel := range rels {
		asset := up.findAsset(rel)
		if asset == nil {
			continue
		}

		v, err := semver.Parse(rel.GetTagName())
		if err != nil {
			log.Println("update:", err)
			continue
		}

		r := &Release{
			v,
			rel.GetPrerelease(),
			rel.GetDraft(),
			asset.GetBrowserDownloadURL(),
			asset.GetSize(),
			asset.GetID(),
			-1,
			rel.GetHTMLURL(),
			rel.GetBody(),
			rel.GetName(),
			rel.GetPublishedAt().Time,
			owner,
			name,
		}
		tmp = append(tmp, r)
	}

	return tmp, nil
}

// DetectVersion tries to get the given version of the repository on Github. `slug` means `owner/name` formatted string.
// And version indicates the required version.
func (up *Updater) DetectVersion(slug string, version string) (release *Release, found bool, err error) {
	owner, name, err := parseSlug(slug)
	if err != nil {
		return nil, false, err
	}

	rels, err := up.fetchReleases(owner, name)
	if err != nil {
		return nil, false, err
	}

	opt := options{pre: up.pre, draft: up.draft}
	rel, asset, ver, found := findReleaseAndAsset(rels, version, up.filters, opt)
	if !found {
		return nil, false, nil
	}

	url := asset.GetBrowserDownloadURL()
	log.Println("Successfully fetched the latest release. tag:", rel.GetTagName(), ", name:", rel.GetName(), ", URL:", rel.GetURL(), ", Asset:", url)

	release = &Release{
		ver,
		rel.GetPrerelease(),
		rel.GetDraft(),
		url,
		asset.GetSize(),
		asset.GetID(),
		-1,
		rel.GetHTMLURL(),
		rel.GetBody(),
		rel.GetName(),
		rel.GetPublishedAt().Time,
		owner,
		name,
	}

	if up.validator != nil {
		validationName := asset.GetName() + up.validator.Suffix()
		validationAsset, ok := findValidationAsset(rel, validationName)
		if !ok {
			return nil, false, fmt.Errorf("Failed finding validation file %q", validationName)
		}
		release.ValidationAssetID = validationAsset.GetID()
	}

	return release, true, nil
}

// DetectLatest detects the latest release of the slug (owner/repo).
// This function is a shortcut version of updater.DetectLatest() method.
func DetectLatest(slug string) (*Release, bool, error) {
	return DefaultUpdater().DetectLatest(slug)
}

// DetectVersion detects the given release of the slug (owner/repo) from its version.
func DetectVersion(slug string, version string) (*Release, bool, error) {
	return DefaultUpdater().DetectVersion(slug, version)
}

func parseSlug(slug string) (owner string, name string, err error) {
	repo := strings.Split(slug, "/")
	if len(repo) != 2 || repo[0] == "" || repo[1] == "" {
		err = fmt.Errorf("Invalid slug format. It should be 'owner/name': %s", slug)
		return
	}

	owner = repo[0]
	name = repo[1]

	return
}
