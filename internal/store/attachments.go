package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ResultAttachment is a file kept with a result.
type ResultAttachment struct {
	ID           int64
	TestResultID int64
	// ReleaseID is the release the result belongs to: the file is served under it, and nowhere
	// else.
	ReleaseID  *int64
	Name       string
	MediaType  string
	Object     string
	SizeBytes  int64
	CaseSuite  string
	CaseName   string
	Attributes map[string]string
}

// ErrAttachmentNotFound reports an attachment that does not exist.
var ErrAttachmentNotFound = errors.New("attachment not found")

const attachmentColumns = `a.id, a.test_result_id, t.release_id, a.name, a.media_type, a.object, a.size_bytes,
	coalesce(a.case_suite, ''), coalesce(a.case_name, ''), a.attributes`

// ResultAttachments lists a result's files, in the order they were recorded.
func (db *DB) ResultAttachments(ctx context.Context, resultID int64) ([]ResultAttachment, error) {
	rows, err := db.pool.Query(ctx, `select `+attachmentColumns+`
		from result_attachments a join test_results t on t.id = a.test_result_id
		where a.test_result_id = $1 order by a.id`, resultID)
	if err != nil {
		return nil, fmt.Errorf("attachments of result %d: %w", resultID, err)
	}
	defer rows.Close()

	attachments := []ResultAttachment{}
	for rows.Next() {
		attachment, err := scanAttachment(rows)
		if err != nil {
			return nil, fmt.Errorf("attachments of result %d: %w", resultID, err)
		}
		attachments = append(attachments, attachment)
	}
	return attachments, rows.Err()
}

// GetAttachment returns one file and the release it belongs to.
func (db *DB) GetAttachment(ctx context.Context, id int64) (ResultAttachment, error) {
	row := db.pool.QueryRow(ctx, `select `+attachmentColumns+`
		from result_attachments a join test_results t on t.id = a.test_result_id
		where a.id = $1`, id)
	attachment, err := scanAttachment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResultAttachment{}, ErrAttachmentNotFound
	}
	if err != nil {
		return ResultAttachment{}, fmt.Errorf("attachment %d: %w", id, err)
	}
	return attachment, nil
}

func scanAttachment(row pgx.Row) (ResultAttachment, error) {
	var attachment ResultAttachment
	err := row.Scan(&attachment.ID, &attachment.TestResultID, &attachment.ReleaseID,
		&attachment.Name, &attachment.MediaType, &attachment.Object, &attachment.SizeBytes,
		&attachment.CaseSuite, &attachment.CaseName, &attachment.Attributes)
	return attachment, err
}
