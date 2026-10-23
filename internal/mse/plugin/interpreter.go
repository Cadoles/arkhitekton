package plugin

import (
	"context"
	"path"
	"regexp"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/interpret"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// e2eReader reads the end-to-end campaign's mochawesome report. Its Pipeline is the operator's, so
// only the report speaks: Cypress's exit code is the number of failed tests, not a verdict on
// whether it ran.
//
// The videos and the screenshots the campaign kept come with its cases: each screenshot beside the
// case that failed, when its name says which.
type e2eReader struct{}

func (e2eReader) Interpret(_ context.Context, _ capability.RunState, records []parse.Record) ([]facts.Set, error) {
	const tool = "cypress"
	attachments := attachmentsOf(records)
	record, readable := interpret.Readable(records, tool)
	if !readable {
		if len(attachments) == 0 {
			return nil, nil
		}
		// No report, but what the campaign recorded still says what happened.
		return []facts.Set{{
			Shape: facts.ShapeAttachments, Group: "e2e", Tool: tool, Status: facts.StatusNA,
			Summary: "no readable report", Attachments: attachments,
		}}, nil
	}
	report := record.Payload.(parse.Mochawesome)
	cases := interpret.MochaCases(report)
	linkScreenshots(attachments, cases)
	return []facts.Set{{
		Shape: facts.ShapeCases, Group: "e2e", Tool: tool, RawObject: record.Object,
		Cases: cases, DurationMS: report.DurationMS, Summary: interpret.SummarizeTests(cases),
		Attachments: attachments,
	}}, nil
}

// attachmentsOf turns what the host kept into attachments, named after the file.
func attachmentsOf(records []parse.Record) []facts.Attachment {
	var attachments []facts.Attachment
	for _, record := range records {
		attached, ok := record.Payload.(parse.Attached)
		if !ok || record.Err != nil {
			continue
		}
		attachment := facts.Attachment{
			Name: path.Base(record.Member), MediaType: attached.MediaType, Object: attached.Object, Size: attached.Size,
		}
		if spec := specOf(record.Member, attached.MediaType); spec != "" {
			attachment.Attributes = map[string]string{"spec": spec}
		}
		attachments = append(attachments, attachment)
	}
	return attachments
}

// specOf is the spec a file comes from, as Cypress files them: a video is named after its spec, a
// screenshot is filed in a folder named after it.
func specOf(member, mediaType string) string {
	if strings.HasPrefix(mediaType, "video/") {
		return strings.TrimSuffix(path.Base(member), path.Ext(member))
	}
	if folder := path.Base(path.Dir(member)); folder != "." && folder != "/" {
		return folder
	}
	return ""
}

// screenshotName reads Cypress's name for a failure's screenshot: the titles joined by ` -- `,
// then ` (failed)`, then the attempt when the test was retried.
var screenshotName = regexp.MustCompile(`^(.+) \(failed\)(?: \(attempt \d+\))?\.png$`)

// linkScreenshots ties each failure's screenshot to the case it shows: same titles, and, when two
// cases share them, the spec the screenshot was filed under.
func linkScreenshots(attachments []facts.Attachment, cases []facts.Case) {
	for index := range attachments {
		match := screenshotName.FindStringSubmatch(attachments[index].Name)
		if match == nil {
			continue
		}
		title := strings.Join(strings.Split(match[1], " -- "), " ")
		spec := attachments[index].Attributes["spec"]
		var found *facts.Case
		for candidate := range cases {
			if cases[candidate].Name != title {
				continue
			}
			if spec != "" && strings.Contains(cases[candidate].Suite, spec) {
				found = &cases[candidate]
				break
			}
			if found == nil {
				found = &cases[candidate]
			}
		}
		if found != nil {
			attachments[index].Case = &facts.CaseRef{Suite: found.Suite, Name: found.Name}
		}
	}
}
