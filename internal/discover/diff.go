package discover

// Per-file diff stats and the repo's generated-file declarations: everything
// author scoring needs about a PR's shape, fetched WITHOUT pulling a patch.
//
// The REST endpoint (`/pulls/{n}/files`) is unusable here. It returns the full
// `patch` text per file and offers no field selection, so filtering client-side
// still pays for every byte. Measured against cli/cli#13541 (13,075 additions):
// REST 341,081 bytes, this GraphQL query 3,032. A 113x difference on exactly
// the PRs whose size we are trying to record.

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/shhac/agent-code-review/internal/score"
)

// filesPerPage is GitHub's maximum for this connection.
const filesPerPage = 100

// maxFilePages bounds the walk. GitHub itself stops reporting files at 3000,
// so 30 pages is its ceiling rather than ours; past it we record the raw
// totals and exclude nothing rather than paging forever.
const maxFilePages = 30

const prFilesQuery = `query($owner:String!, $repo:String!, $number:Int!, $cursor:String) {
  repository(owner:$owner, name:$repo) {
    pullRequest(number:$number) {
      headRefOid
      additions
      deletions
      changedFiles
      files(first:100, after:$cursor) {
        pageInfo { hasNextPage endCursor }
        nodes { path additions deletions }
      }
    }
  }
}`

type ghFilesResp struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				HeadRefOID   string `json:"headRefOid"`
				Additions    int    `json:"additions"`
				Deletions    int    `json:"deletions"`
				ChangedFiles int    `json:"changedFiles"`
				Files        struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []score.FileStat `json:"nodes"`
				} `json:"files"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// PRDiff is a PR's size, as GitHub reports it and per file.
//
// HeadSHA is the revision these figures describe. A PR's head can advance
// while a review runs, so a caller must check it against the head it actually
// reviewed before crediting anybody with these lines.
type PRDiff struct {
	HeadSHA      string
	Additions    int
	Deletions    int
	ChangedFiles int
	Files        []score.FileStat
	// Truncated means GitHub stopped listing files before the end, so Files
	// is incomplete and no exclusion computed from it can be trusted.
	Truncated bool
}

// PRFiles fetches a PR's per-file line counts, paging the files connection.
func PRFiles(ctx context.Context, repo string, number int) (PRDiff, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return PRDiff{}, err
	}

	var out PRDiff
	cursor := ""
	for page := 0; ; page++ {
		if page >= maxFilePages {
			out.Truncated = true
			break
		}
		args := []string{"api", "graphql",
			"-f", "owner=" + owner,
			"-f", "repo=" + name,
			"-F", fmt.Sprintf("number=%d", number),
			"-f", "query=" + prFilesQuery,
		}
		// gh has no spelling for a null String variable, so the first page
		// omits the variable entirely rather than passing "null" (which would
		// arrive as the four-character string).
		if cursor != "" {
			args = append(args, "-f", "cursor="+cursor)
		}
		raw, err := runGH(ctx, args...)
		if err != nil {
			return PRDiff{}, err
		}
		var resp ghFilesResp
		if err := json.Unmarshal(raw, &resp); err != nil {
			return PRDiff{}, fmt.Errorf("decode pr files for %s#%d: %w", repo, number, err)
		}
		pr := resp.Data.Repository.PullRequest
		out.HeadSHA = pr.HeadRefOID
		out.Additions, out.Deletions, out.ChangedFiles = pr.Additions, pr.Deletions, pr.ChangedFiles
		out.Files = append(out.Files, pr.Files.Nodes...)

		if !pr.Files.PageInfo.HasNextPage || pr.Files.PageInfo.EndCursor == "" {
			break
		}
		cursor = pr.Files.PageInfo.EndCursor
	}
	return out, nil
}

// attrsQuery fetches several .gitattributes blobs in one round trip, one
// alias per candidate directory. Paths arrive as VARIABLES, never interpolated
// into the document.
func attrsQuery(n int) string {
	var b strings.Builder
	b.WriteString("query($owner:String!, $repo:String!")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, ", $e%d:String!", i)
	}
	b.WriteString(") {\n  repository(owner:$owner, name:$repo) {\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "    a%d: object(expression:$e%d) { ... on Blob { text } }\n", i, i)
	}
	b.WriteString("  }\n}")
	return b.String()
}

// GitAttributes fetches the repo's .gitattributes declarations covering the
// given changed files: the root one, plus one per directory those files live
// in. Absent files come back null, which is the common case and not an error.
//
// Read from the BASE branch rather than the PR's head. `repository(owner,name)`
// resolves against the base repo, where a fork's head commit is usually but not
// reliably reachable; the base branch is also the repo's own standing
// declaration of what counts as generated, which is the thing worth honouring.
// A PR that edits .gitattributes therefore scores under the old declarations
// until it lands, which is the correct direction for a rule about what already
// exists.
func GitAttributes(ctx context.Context, repo, ref string, files []score.FileStat) (map[string]string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	dirs := attrDirs(files)
	if len(dirs) == 0 {
		return nil, nil
	}
	if ref == "" {
		ref = "HEAD"
	}

	args := []string{"api", "graphql", "-f", "owner=" + owner, "-f", "repo=" + name}
	for i, dir := range dirs {
		expr := ref + ":" + path.Join(dir, ".gitattributes")
		args = append(args, "-f", fmt.Sprintf("e%d=%s", i, expr))
	}
	args = append(args, "-f", "query="+attrsQuery(len(dirs)))

	raw, err := runGH(ctx, args...)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Repository map[string]*struct {
				Text string `json:"text"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode gitattributes for %s: %w", repo, err)
	}

	out := map[string]string{}
	for i, dir := range dirs {
		blob := resp.Data.Repository[fmt.Sprintf("a%d", i)]
		if blob != nil && blob.Text != "" {
			out[dir] = blob.Text
		}
	}
	return out, nil
}

// attrDirs is every directory a .gitattributes could live in and still govern
// one of these files: the repo root, plus every ANCESTOR of each changed
// file's directory.
//
// Ancestors included, which they were not at first. Skipping them saved a
// handful of aliases and quietly broke git's own rule: a declaration in
// a/.gitattributes governs a/b/c/file.go, so a repo marking
// `generated/** linguist-generated` one level down had those files counted as
// hand-written churn. The whole premise here is that we honour the repo's
// declaration, so honouring most of it is not a saving worth having.
//
// Still bounded by the PR's own shape rather than the repo's size: the set is
// the union of the ancestor chains of the changed files, deduped, so a wide
// PR in a shallow tree costs a handful of aliases and a deep one costs its
// depth.
func attrDirs(files []score.FileStat) []string {
	// No changed files means nothing to exclude, so there is nothing to ask
	// about. Returning the root anyway made GitAttributes' own "nothing to
	// fetch" guard unreachable and spent an API call to learn it.
	if len(files) == 0 {
		return nil
	}
	seen := map[string]bool{"": true}
	dirs := []string{""}
	for _, f := range files {
		dir := path.Dir(strings.TrimPrefix(f.Path, "/"))
		for dir != "." && dir != "/" && dir != "" {
			if !seen[dir] {
				seen[dir] = true
				dirs = append(dirs, dir)
			}
			dir = path.Dir(dir)
		}
	}
	sort.Strings(dirs)
	return dirs
}
