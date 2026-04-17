package models

import "time"

// ─── Ollama AI Response Structures ───────────────────────────────────────────

// CaseJSON is what the AI returns for each case (or chunk of a case)
type CaseJSON struct {
	CaseNumber   string      `json:"case_number"`
	CaseDate     string      `json:"case_date"`
	CourtName    string      `json:"court_name"`
	CaseSubject  string      `json:"case_subject"`
	CaseYear     string      `json:"case_year"`
	Defendants   []Defendant `json:"defendants"`
	Verdicts     []Verdict   `json:"verdicts"`
}

type Defendant struct {
	Name       string   `json:"name"`
	NationalID string   `json:"national_id"`
	JobTitle   string   `json:"job_title"`
	Workplace  string   `json:"workplace"`
	Charges    []string `json:"charges"`
}

type Verdict struct {
	DefendantNames []string `json:"defendant_names"`
	VerdictText    string   `json:"verdict_text"`
	VerdictType    string   `json:"verdict_type"` // إدانة / براءة / تأجيل
}

// ─── Database Structures ─────────────────────────────────────────────────────

type ProcessingStatus string

const (
	StatusPending      ProcessingStatus = "pending"
	StatusProcessing   ProcessingStatus = "processing"
	StatusCompleted    ProcessingStatus = "completed"
	StatusNeedsReview  ProcessingStatus = "needs_review"
	StatusFailed       ProcessingStatus = "failed"
)

type Case struct {
	ID          int64            `db:"id"`
	FileName    string           `db:"file_name"`    // "2233333.pdf"
	CaseNumber  string           `db:"case_number"`  // "2233333"
	CaseDate    *time.Time       `db:"case_date"`
	CourtName   string           `db:"court_name"`
	CaseSubject string           `db:"case_subject"`
	CaseYear    string           `db:"case_year"`
	RawJSON     string           `db:"raw_json"`     // الـ JSON الكامل من الـ AI
	Status      ProcessingStatus `db:"status"`
	RetryCount  int              `db:"retry_count"`
	ErrorMsg    string           `db:"error_msg"`
	CreatedAt   time.Time        `db:"created_at"`
	UpdatedAt   time.Time        `db:"updated_at"`
}

type CaseDefendant struct {
	ID         int64  `db:"id"`
	CaseID     int64  `db:"case_id"`
	Name       string `db:"name"`
	NationalID string `db:"national_id"`
	JobTitle   string `db:"job_title"`
	Workplace  string `db:"workplace"`
	Charges    string `db:"charges"` // JSON array as string
}

type CaseVerdict struct {
	ID             int64  `db:"id"`
	CaseID         int64  `db:"case_id"`
	DefendantNames string `db:"defendant_names"` // JSON array as string
	VerdictText    string `db:"verdict_text"`
	VerdictType    string `db:"verdict_type"`
}
