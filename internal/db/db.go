package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/microsoft/go-mssqldb"

	"github.com/sirayoub/case-processor/internal/models"
)

type DB struct {
	conn *sqlx.DB
}

func New(connStr string) (*DB, error) {
	conn, err := sqlx.Connect("sqlserver", connStr)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(5 * time.Minute)
	return &DB{conn: conn}, nil
}

func (d *DB) Close() { d.conn.Close() }

// ─── Schema Creation ──────────────────────────────────────────────────────────

func (d *DB) CreateSchema(ctx context.Context) error {
	queries := []string{
		`IF NOT EXISTS (SELECT * FROM sysobjects WHERE name='cases' AND xtype='U')
		CREATE TABLE cases (
			id            BIGINT IDENTITY(1,1) PRIMARY KEY,
			file_name     NVARCHAR(255) NOT NULL UNIQUE,
			case_number   NVARCHAR(100),
			case_date     DATE,
			court_name    NVARCHAR(500),
			case_subject  NVARCHAR(MAX),
			case_year     NVARCHAR(20),
			raw_json      NVARCHAR(MAX),
			status        NVARCHAR(50) NOT NULL DEFAULT 'pending',
			retry_count   INT NOT NULL DEFAULT 0,
			error_msg     NVARCHAR(MAX),
			created_at    DATETIME2 NOT NULL DEFAULT GETDATE(),
			updated_at    DATETIME2 NOT NULL DEFAULT GETDATE()
		)`,
		`IF NOT EXISTS (SELECT * FROM sysobjects WHERE name='case_defendants' AND xtype='U')
		CREATE TABLE case_defendants (
			id           BIGINT IDENTITY(1,1) PRIMARY KEY,
			case_id      BIGINT NOT NULL REFERENCES cases(id) ON DELETE CASCADE,
			name         NVARCHAR(500),
			national_id  NVARCHAR(50),
			job_title    NVARCHAR(500),
			workplace    NVARCHAR(500),
			charges      NVARCHAR(MAX)
		)`,
		`IF NOT EXISTS (SELECT * FROM sysobjects WHERE name='case_verdicts' AND xtype='U')
		CREATE TABLE case_verdicts (
			id              BIGINT IDENTITY(1,1) PRIMARY KEY,
			case_id         BIGINT NOT NULL REFERENCES cases(id) ON DELETE CASCADE,
			defendant_names NVARCHAR(MAX),
			verdict_text    NVARCHAR(MAX),
			verdict_type    NVARCHAR(100)
		)`,
		// Index for fast lookup by status (for processing queue)
		`IF NOT EXISTS (SELECT * FROM sys.indexes WHERE name='IX_cases_status')
		CREATE INDEX IX_cases_status ON cases(status)`,
	}

	for _, q := range queries {
		if _, err := d.conn.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("schema creation failed: %w\nQuery: %s", err, q[:50])
		}
	}
	return nil
}

// ─── Case Operations ──────────────────────────────────────────────────────────

// UpsertCaseFile registers a new PDF file for processing (idempotent)
func (d *DB) UpsertCaseFile(ctx context.Context, fileName string) (int64, error) {
	var id int64
	err := d.conn.QueryRowContext(ctx, `
		MERGE cases AS target
		USING (SELECT @p1 AS file_name) AS source ON target.file_name = source.file_name
		WHEN NOT MATCHED THEN
			INSERT (file_name, status, created_at, updated_at)
			VALUES (@p1, 'pending', GETDATE(), GETDATE())
		WHEN MATCHED THEN
			UPDATE SET updated_at = GETDATE()
		OUTPUT INSERTED.id;
	`, fileName).Scan(&id)
	return id, err
}

// GetPendingCases returns cases that need processing
func (d *DB) GetPendingCases(ctx context.Context, limit int) ([]models.Case, error) {
	var cases []models.Case
	err := d.conn.SelectContext(ctx, &cases, `
		SELECT TOP (@p1) id, file_name, status, retry_count
		FROM cases
		WHERE status IN ('pending', 'failed')
		  AND retry_count < 2
		ORDER BY created_at ASC
	`, limit)
	return cases, err
}

// UpdateCaseStatus updates case processing status
func (d *DB) UpdateCaseStatus(ctx context.Context, id int64, status models.ProcessingStatus, errMsg string) error {
	_, err := d.conn.ExecContext(ctx, `
		UPDATE cases SET status = @p2, error_msg = @p3, updated_at = GETDATE()
		WHERE id = @p1
	`, id, string(status), errMsg)
	return err
}

// IncrementRetry increments retry counter and sets status to failed
func (d *DB) IncrementRetry(ctx context.Context, id int64, errMsg string) error {
	_, err := d.conn.ExecContext(ctx, `
		UPDATE cases
		SET retry_count = retry_count + 1,
		    status = CASE WHEN retry_count + 1 >= 2 THEN 'needs_review' ELSE 'failed' END,
		    error_msg = @p2,
		    updated_at = GETDATE()
		WHERE id = @p1
	`, id, errMsg)
	return err
}

// SaveCaseData saves the extracted case data from AI
func (d *DB) SaveCaseData(ctx context.Context, caseID int64, data *models.CaseJSON) error {
	tx, err := d.conn.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Parse case date
	var caseDate interface{}
	if data.CaseDate != "" {
		caseDate = data.CaseDate
	}

	// Serialize full JSON for audit
	rawJSON, _ := json.Marshal(data)

	// Update case main record
	_, err = tx.ExecContext(ctx, `
		UPDATE cases SET
			case_number  = @p2,
			case_date    = @p3,
			court_name   = @p4,
			case_subject = @p5,
			case_year    = @p6,
			raw_json     = @p7,
			status       = 'completed',
			updated_at   = GETDATE()
		WHERE id = @p1
	`, caseID, data.CaseNumber, caseDate, data.CourtName, data.CaseSubject, data.CaseYear, string(rawJSON))
	if err != nil {
		return fmt.Errorf("update case: %w", err)
	}

	// Delete old defendants/verdicts (in case of retry)
	tx.ExecContext(ctx, `DELETE FROM case_defendants WHERE case_id = @p1`, caseID)
	tx.ExecContext(ctx, `DELETE FROM case_verdicts WHERE case_id = @p1`, caseID)

	// Insert defendants
	for _, def := range data.Defendants {
		chargesJSON, _ := json.Marshal(def.Charges)
		_, err = tx.ExecContext(ctx, `
			INSERT INTO case_defendants (case_id, name, national_id, job_title, workplace, charges)
			VALUES (@p1, @p2, @p3, @p4, @p5, @p6)
		`, caseID, def.Name, def.NationalID, def.JobTitle, def.Workplace, string(chargesJSON))
		if err != nil {
			return fmt.Errorf("insert defendant %s: %w", def.Name, err)
		}
	}

	// Insert verdicts
	for _, v := range data.Verdicts {
		namesJSON, _ := json.Marshal(v.DefendantNames)
		_, err = tx.ExecContext(ctx, `
			INSERT INTO case_verdicts (case_id, defendant_names, verdict_text, verdict_type)
			VALUES (@p1, @p2, @p3, @p4)
		`, caseID, string(namesJSON), v.VerdictText, v.VerdictType)
		if err != nil {
			return fmt.Errorf("insert verdict: %w", err)
		}
	}

	return tx.Commit()
}

// Stats returns processing statistics
func (d *DB) Stats(ctx context.Context) (map[string]int, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT status, COUNT(*) as cnt FROM cases GROUP BY status
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := map[string]int{}
	for rows.Next() {
		var status string
		var cnt int
		rows.Scan(&status, &cnt)
		stats[status] = cnt
	}
	return stats, nil
}
