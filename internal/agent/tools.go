package agent

import (
	"context"
	"net/url"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// tool adds a tool whose call is the API request build makes of its input.
func tool[In any](server *mcp.Server, c *caller, t *mcp.Tool, build func(In) request) {
	mcp.AddTool(server, t, func(ctx context.Context, call *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		return c.call(ctx, call, build(in))
	})
}

func readOnly(name, description string) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{
		ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false),
	}}
}

func acting(name, description string, destructive bool) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{
		DestructiveHint: ptr(destructive), OpenWorldHint: ptr(true),
	}}
}

func ptr[T any](v T) *T { return &v }

// set adds a parameter to a query when it has a value.
func set(query url.Values, name, value string) url.Values {
	if value != "" {
		query.Set(name, value)
	}
	return query
}

func setInt(query url.Values, name string, value int) url.Values {
	if value != 0 {
		query.Set(name, strconv.Itoa(value))
	}
	return query
}

type none struct{}

type projectIn struct {
	Project string `json:"project" jsonschema:"the project's slug, as whoami lists it"`
}

type releaseIn struct {
	Project string `json:"project" jsonschema:"the project's slug"`
	Tag     string `json:"tag" jsonschema:"the release's tag"`
}

type runsIn struct {
	Limit int `json:"limit,omitempty" jsonschema:"how many runs, newest first (default 100)"`
}

type runIn struct {
	Run string `json:"run" jsonschema:"the run's name"`
}

type logIn struct {
	Run    string `json:"run" jsonschema:"the run's name"`
	Task   string `json:"task" jsonschema:"the task's name, as get_run lists it"`
	Offset *int   `json:"offset,omitempty" jsonschema:"read from this byte; without it, the end of the log is read"`
	Length int    `json:"length,omitempty" jsonschema:"how many bytes to read (default 32768)"`
}

type queueIn struct {
	Project string `json:"project,omitempty" jsonschema:"a project's slug, for the values its pipelines' inputs suggest"`
}

type testsIn struct {
	Project string `json:"project" jsonschema:"the project's slug"`
	Tag     string `json:"tag" jsonschema:"the release's tag"`
	Group   string `json:"group,omitempty" jsonschema:"a group of results, to list its test cases"`
	Offset  int    `json:"offset,omitempty" jsonschema:"with group: the first test case to list"`
	Query   string `json:"query,omitempty" jsonschema:"with group: test cases whose name contains this"`
}

type securityIn struct {
	Project    string `json:"project" jsonschema:"the project's slug"`
	Tag        string `json:"tag" jsonschema:"the release's tag"`
	Capability string `json:"capability,omitempty" jsonschema:"one pipeline's audit, by its capability"`
}

type pullsIn struct {
	Project string `json:"project" jsonschema:"the project's slug"`
	State   string `json:"state,omitempty" jsonschema:"open or closed (closed includes merged); all without it"`
}

type pullIn struct {
	Project string `json:"project" jsonschema:"the project's slug"`
	Number  int    `json:"number" jsonschema:"the pull request's number"`
}

type versionIn struct {
	Project string `json:"project" jsonschema:"the project's slug"`
	ID      int    `json:"id" jsonschema:"the version's identifier"`
}

type statsIn struct {
	Project string `json:"project,omitempty" jsonschema:"a project's slug; without it, every project the person operates"`
	Range   string `json:"range,omitempty" jsonschema:"30d or 90d, by week, or 12m, by month (default 30d)"`
}

type journalIn struct {
	Nature  string `json:"nature,omitempty" jsonschema:"action, event or automation"`
	Project string `json:"project,omitempty" jsonschema:"a project's slug"`
	Query   string `json:"query,omitempty" jsonschema:"searched in an action's name and target, an event's type and subject, a run's trigger and name"`
	Limit   int    `json:"limit,omitempty" jsonschema:"how many lines, newest first (default 100, 500 at most)"`
}

const defaultLogLength = 32 << 10

func addReadTools(server *mcp.Server, c *caller) {
	tool(server, c, readOnly("whoami",
		"The person you act for: their role on the instance and in each project, and the projects with their slugs."),
		func(none) request { return get("/me", nil) })

	tool(server, c, readOnly("list_runs", "The most recent pipeline runs, newest first, with their state."),
		func(in runsIn) request { return get("/runs", setInt(url.Values{}, "limit", in.Limit)) })
	tool(server, c, readOnly("get_run", "A run: its tasks, their states and durations."),
		func(in runIn) request { return get(p("runs", in.Run), nil) })
	tool(server, c, readOnly("read_task_log",
		"A task's log. Without an offset, its end is read: that is where a failure explains itself."),
		func(in logIn) request {
			length := in.Length
			if length <= 0 {
				length = defaultLogLength
			}
			query := setInt(url.Values{}, "length", length)
			if in.Offset != nil {
				query.Set("offset", strconv.Itoa(*in.Offset))
			}
			return get(p("runs", in.Run, "tasks", in.Task, "log"), query)
		})
	tool(server, c, readOnly("get_queue",
		"The queue: the jobs running and waiting, in the order of service, and why each waits; the pipeline kinds "+
			"that can be launched, with their inputs."),
		func(in queueIn) request { return get("/queue", set(url.Values{}, "project", in.Project)) })

	tool(server, c, readOnly("list_releases", "A project's releases, with their state."),
		func(in projectIn) request { return get(p("projects", in.Project, "releases"), nil) })
	tool(server, c, readOnly("get_release",
		"A release: its state, its git reference, its builds, its verifications and what can be done with it."),
		func(in releaseIn) request { return get(p("projects", in.Project, "releases", in.Tag), nil) })
	tool(server, c, readOnly("get_release_notes", "A release's note: the issues it delivers."),
		func(in releaseIn) request { return get(p("projects", in.Project, "releases", in.Tag, "notes"), nil) })
	tool(server, c, readOnly("get_test_results",
		"A release's test results by group; with a group, its test cases and their failures."),
		func(in testsIn) request {
			path := p("projects", in.Project, "releases", in.Tag, "tests")
			if in.Group == "" {
				return get(path, nil)
			}
			query := set(setInt(url.Values{}, "offset", in.Offset), "q", in.Query)
			return get(path+p(in.Group), query)
		})
	tool(server, c, readOnly("get_security_report", "A release's security audits and their findings."),
		func(in securityIn) request {
			return get(p("projects", in.Project, "releases", in.Tag, "security"),
				set(url.Values{}, "capability", in.Capability))
		})
	tool(server, c, readOnly("get_bench", "A release's performance measures, compared with their baselines."),
		func(in releaseIn) request { return get(p("projects", in.Project, "releases", in.Tag, "bench"), nil) })

	tool(server, c, readOnly("list_pull_requests", "A project's pull requests, as the dashboard follows them."),
		func(in pullsIn) request {
			return get(p("projects", in.Project, "pulls"), set(url.Values{}, "state", in.State))
		})
	tool(server, c, readOnly("get_pull_request",
		"A pull request, the jobs launched on it and the automations its events set off."),
		func(in pullIn) request {
			return get(p("projects", in.Project, "pulls", strconv.Itoa(in.Number)), nil)
		})

	tool(server, c, readOnly("list_versions", "A project's versions, as its tracker plans them."),
		func(in projectIn) request { return get(p("projects", in.Project, "versions"), nil) })
	tool(server, c, readOnly("get_version", "A version and its issues."),
		func(in versionIn) request {
			return get(p("projects", in.Project, "versions", strconv.Itoa(in.ID)), nil)
		})
	tool(server, c, readOnly("list_forge_refs",
		"A project's branches and tags on its forge, to choose what a release builds."),
		func(in projectIn) request { return get(p("projects", in.Project, "forge", "refs"), nil) })

	tool(server, c, readOnly("get_statistics",
		"The releases launched and the pipelines finished over a period: counts, failures, median durations."),
		func(in statsIn) request {
			return get("/stats", set(set(url.Values{}, "project", in.Project), "range", in.Range))
		})
	tool(server, c, readOnly("read_journal",
		"What happened, newest first: people's actions, events received and the automations they set off. "+
			"Administrators only."),
		func(in journalIn) request {
			query := set(set(set(url.Values{}, "nature", in.Nature), "project", in.Project), "q", in.Query)
			return get("/admin/journal", setInt(query, "limit", in.Limit))
		})
}

type launchIn struct {
	Kind     string            `json:"kind" jsonschema:"the pipeline kind, as get_queue lists it"`
	Params   map[string]string `json:"params,omitempty" jsonschema:"the pipeline's inputs, by name"`
	Project  string            `json:"project,omitempty" jsonschema:"the project's slug the run is for"`
	Priority int               `json:"priority,omitempty" jsonschema:"a higher priority is served first"`
}

type cancelIn struct {
	ID int `json:"id" jsonschema:"the job's identifier, as get_queue lists it"`
}

type createReleaseIn struct {
	Project        string `json:"project" jsonschema:"the project's slug"`
	Tag            string `json:"tag" jsonschema:"the release's tag"`
	Kind           string `json:"kind" jsonschema:"the release's kind, among those the project declares"`
	GitRef         string `json:"git_ref,omitempty" jsonschema:"the branch, tag or commit to build"`
	TrackedVersion string `json:"tracked_version,omitempty" jsonschema:"the tracker's version aimed at, checked against the tag"`
	SourceTag      string `json:"source_tag,omitempty" jsonschema:"for a final release: the pre-release it re-tags"`
}

type releasePipelineIn struct {
	Project string            `json:"project" jsonschema:"the project's slug"`
	Tag     string            `json:"tag" jsonschema:"the release's tag"`
	Kind    string            `json:"kind" jsonschema:"the pipeline kind, as get_queue lists it"`
	Params  map[string]string `json:"params,omitempty" jsonschema:"the inputs the release does not fill, by name"`
}

type pullPipelineIn struct {
	Project string            `json:"project" jsonschema:"the project's slug"`
	Number  int               `json:"number" jsonschema:"the pull request's number"`
	Kind    string            `json:"kind" jsonschema:"the pipeline kind, as get_queue lists it"`
	Params  map[string]string `json:"params,omitempty" jsonschema:"the inputs the pull request does not fill, by name"`
}

type pipelineBody struct {
	Kind   string            `json:"kind"`
	Params map[string]string `json:"params"`
}

func addRunTools(server *mcp.Server, c *caller) {
	tool(server, c, acting("launch_pipeline", "Queues a pipeline run.", false),
		func(in launchIn) request {
			return post("/queue", map[string]any{
				"kind": in.Kind, "params": in.Params, "project": in.Project, "priority": in.Priority,
			})
		})
	tool(server, c, acting("cancel_job", "Cancels a job, waiting or running.", true),
		func(in cancelIn) request { return post(p("queue", strconv.Itoa(in.ID), "cancel"), nil) })

	tool(server, c, acting("create_release",
		"Creates a release as a draft: its note is detected and nothing is built until launch_release.", false),
		func(in createReleaseIn) request {
			return post(p("projects", in.Project, "releases"), map[string]any{
				"tag": in.Tag, "kind": in.Kind, "git_ref": in.GitRef,
				"tracked_version": in.TrackedVersion, "source_tag": in.SourceTag,
			})
		})
	tool(server, c, acting("launch_release",
		"Launches a draft release: resolves its reference, sets its git tag and starts its build and automations.", false),
		func(in releaseIn) request { return post(p("projects", in.Project, "releases", in.Tag, "launch"), nil) })
	tool(server, c, acting("rebuild_release",
		"Builds a launched release again on the same reference, after an infrastructure failure.", false),
		func(in releaseIn) request { return post(p("projects", in.Project, "releases", in.Tag, "build"), nil) })
	tool(server, c, acting("run_release_pipeline",
		"Queues a pipeline on a launched release: its revision, tag and images are filled.", false),
		func(in releasePipelineIn) request {
			return post(p("projects", in.Project, "releases", in.Tag, "run"),
				pipelineBody{Kind: in.Kind, Params: in.Params})
		})
	tool(server, c, acting("approve_release",
		"Approves a release: commits what will reach the users. Requires the right to produce a final release.", true),
		func(in releaseIn) request { return post(p("projects", in.Project, "releases", in.Tag, "release"), nil) })
	tool(server, c, acting("launch_pull_request_pipeline",
		"Queues a pipeline on a pull request's head: its revision, tag and images are filled.", false),
		func(in pullPipelineIn) request {
			return post(p("projects", in.Project, "pulls", strconv.Itoa(in.Number), "launch"),
				pipelineBody{Kind: in.Kind, Params: in.Params})
		})
}
