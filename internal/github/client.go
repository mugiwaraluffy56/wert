package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	token string
	org   string
	http  *http.Client
}

func New(token, org string) *Client {
	return &Client{
		token: token,
		org:   org,
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) IsConfigured() bool {
	return c.token != "" && c.org != ""
}

func (c *Client) Org() string { return c.org }

// ---- data types ----

type Repo struct {
	Name        string    `json:"name"`
	FullName    string    `json:"full_name"`
	Description string    `json:"description"`
	Stars       int       `json:"stargazers_count"`
	OpenIssues  int       `json:"open_issues_count"`
	PushedAt    time.Time `json:"pushed_at"`
	HTMLURL     string    `json:"html_url"`
	Private     bool      `json:"private"`
}

type PR struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Login     string    `json:"-"`
	State     string    `json:"state"`
	Draft     bool      `json:"draft"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	HTMLURL   string    `json:"html_url"`
	RepoName  string    `json:"-"`
	Labels    []string  `json:"-"`
}

type Issue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Login     string    `json:"-"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	HTMLURL   string    `json:"html_url"`
	RepoName  string    `json:"-"`
	Labels    []string  `json:"-"`
}

type OrgMember struct {
	Login string `json:"login"`
}

type OrgData struct {
	Repos     []Repo
	PRs       []PR
	Issues    []Issue
	Members   []OrgMember
	FetchedAt time.Time
}

// ---- fetch ----

func (c *Client) FetchAll() (*OrgData, error) {
	data := &OrgData{FetchedAt: time.Now()}

	repos, err := c.fetchRepos()
	if err != nil {
		return nil, fmt.Errorf("repos: %w", err)
	}
	data.Repos = repos

	prs, err := c.searchItems("is:pr is:open")
	if err != nil {
		return nil, fmt.Errorf("prs: %w", err)
	}
	data.PRs = toPRs(prs)

	issues, err := c.searchItems("is:issue is:open")
	if err != nil {
		return nil, fmt.Errorf("issues: %w", err)
	}
	data.Issues = toIssues(issues)

	members, _ := c.fetchMembers() // non-fatal
	data.Members = members

	return data, nil
}

func (c *Client) fetchRepos() ([]Repo, error) {
	var repos []Repo
	err := c.get(fmt.Sprintf("/orgs/%s/repos?sort=pushed&per_page=20", c.org), &repos)
	return repos, err
}

func (c *Client) fetchMembers() ([]OrgMember, error) {
	var members []OrgMember
	err := c.get(fmt.Sprintf("/orgs/%s/members?per_page=30", c.org), &members)
	return members, err
}

// rawSearchItem is used only for parsing; we convert to PR/Issue after.
type rawSearchItem struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	HTMLURL     string    `json:"html_url"`
	RepoURL     string    `json:"repository_url"`
	Draft       bool      `json:"draft"`
	User        struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest *struct{} `json:"pull_request"` // nil for issues
}

type searchResp struct {
	TotalCount int              `json:"total_count"`
	Items      []rawSearchItem  `json:"items"`
}

func (c *Client) searchItems(qualifier string) ([]rawSearchItem, error) {
	q := fmt.Sprintf("org:%s %s sort:updated", c.org, qualifier)
	path := "/search/issues?per_page=30&q=" + urlEncode(q)
	var resp searchResp
	if err := c.get(path, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

func toPRs(items []rawSearchItem) []PR {
	out := make([]PR, 0, len(items))
	for _, it := range items {
		labels := make([]string, len(it.Labels))
		for i, l := range it.Labels {
			labels[i] = l.Name
		}
		out = append(out, PR{
			Number:    it.Number,
			Title:     it.Title,
			Login:     it.User.Login,
			State:     it.State,
			Draft:     it.Draft,
			CreatedAt: it.CreatedAt,
			UpdatedAt: it.UpdatedAt,
			HTMLURL:   it.HTMLURL,
			RepoName:  repoNameFromURL(it.RepoURL),
			Labels:    labels,
		})
	}
	return out
}

func toIssues(items []rawSearchItem) []Issue {
	out := make([]Issue, 0, len(items))
	for _, it := range items {
		labels := make([]string, len(it.Labels))
		for i, l := range it.Labels {
			labels[i] = l.Name
		}
		out = append(out, Issue{
			Number:    it.Number,
			Title:     it.Title,
			Login:     it.User.Login,
			State:     it.State,
			CreatedAt: it.CreatedAt,
			UpdatedAt: it.UpdatedAt,
			HTMLURL:   it.HTMLURL,
			RepoName:  repoNameFromURL(it.RepoURL),
			Labels:    labels,
		})
	}
	return out
}

// ---- HTTP helper ----

func (c *Client) get(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("github API %d: %s", resp.StatusCode, body)
	}
	return json.Unmarshal(body, out)
}

// ---- helpers ----

func repoNameFromURL(u string) string {
	parts := strings.Split(u, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return u
}

func urlEncode(s string) string {
	var b strings.Builder
	for _, ch := range s {
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteRune(ch)
		case ch == ' ':
			b.WriteByte('+')
		default:
			b.WriteString(fmt.Sprintf("%%%02X", ch))
		}
	}
	return b.String()
}

// TimeAgo returns a human-readable relative time string.
func TimeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 2")
	}
}
