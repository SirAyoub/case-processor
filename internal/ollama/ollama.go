package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sirayoub/case-processor/internal/models"
	"github.com/sirayoub/case-processor/internal/pdf"
)

type Client struct {
	endpoint string
	model    string
	http     *http.Client
}

func New(endpoint, model string) *Client {
	return &Client{
		endpoint: endpoint,
		model:    model,
		http: &http.Client{
			Timeout: 10 * time.Minute, // Vision inference can be slow
		},
	}
}

// ─── Ollama API Types ─────────────────────────────────────────────────────────

type ollamaMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"` // base64 strings
}

type ollamaRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Options  map[string]any  `json:"options,omitempty"`
}

type ollamaResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Error string `json:"error,omitempty"`
}

// ─── Prompts ──────────────────────────────────────────────────────────────────

const systemPrompt = `أنت نظام استخراج بيانات متخصص في تحليل وثائق القضايا القانونية المصرية.
مهمتك استخراج المعلومات من صور صفحات القضية وإرجاعها بصيغة JSON فقط بدون أي نص إضافي.
القضايا مكتوبة باللغة العربية وقد تكون صور مسحوح ضوئياً (scanned).
التزم بالصيغة المطلوبة تماماً حتى لو بعض المعلومات غير موجودة - ضع قيمة فارغة "".`

func buildExtractionPrompt(isChunk bool, chunkNum, totalChunks int) string {
	chunkNote := ""
	if isChunk {
		chunkNote = fmt.Sprintf(
			"\nملاحظة: هذا الجزء %d من %d من القضية. استخرج فقط المتهمين والأحكام الموجودين في هذه الصفحات.",
			chunkNum, totalChunks,
		)
	}

	return fmt.Sprintf(`%s
قم بتحليل صور صفحات القضية وأرجع JSON بالهيكل التالي فقط:

{
  "case_number": "رقم القضية",
  "case_date": "تاريخ القضية بصيغة YYYY-MM-DD أو فارغ",
  "court_name": "اسم المحكمة",
  "case_subject": "موضوع القضية",
  "case_year": "سنة القضية",
  "defendants": [
    {
      "name": "اسم المتهم الكامل",
      "national_id": "الرقم القومي إن وجد",
      "job_title": "الوظيفة",
      "workplace": "جهة العمل",
      "charges": ["التهمة الأولى", "التهمة الثانية"]
    }
  ],
  "verdicts": [
    {
      "defendant_names": ["اسم المتهم 1", "اسم المتهم 2"],
      "verdict_text": "نص الحكم كاملاً",
      "verdict_type": "إدانة أو براءة أو تأجيل أو غيره"
    }
  ]
}

قواعد مهمة:
- إذا الحكم ينطبق على أكثر من متهم ضعهم في نفس عنصر verdict
- إذا لم تجد معلومة معينة ضع قيمة فارغة "" وليس null
- لا تضع أي نص قبل أو بعد JSON
- تأكد أن JSON صالح قابل للتحليل`, chunkNote)
}

// ─── Main Extraction Function ─────────────────────────────────────────────────

// ExtractCaseData sends pages to Ollama and returns structured case data
func (c *Client) ExtractCaseData(ctx context.Context, pages []pdf.PageImage, chunkNum, totalChunks int) (*models.CaseJSON, error) {
	isChunk := totalChunks > 1

	// Build images list (base64 only, no prefix - Ollama handles it)
	images := make([]string, len(pages))
	for i, p := range pages {
		images[i] = p.Base64
	}

	req := ollamaRequest{
		Model:  c.model,
		Stream: false,
		Options: map[string]any{
			"temperature": 0.1, // Low temperature for consistent structured output
			"num_ctx":     32768,
		},
		Messages: []ollamaMessage{
			{
				Role:    "system",
				Content: systemPrompt,
			},
			{
				Role:    "user",
				Content: buildExtractionPrompt(isChunk, chunkNum, totalChunks),
				Images:  images,
			},
		},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		c.endpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var ollamaResp ollamaResponse
	if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w\nBody: %s", err, string(respBody[:min(200, len(respBody))]))
	}

	if ollamaResp.Error != "" {
		return nil, fmt.Errorf("ollama error: %s", ollamaResp.Error)
	}

	return parseJSON(ollamaResp.Message.Content)
}

// parseJSON extracts and parses JSON from AI response
// Handles cases where model adds text around JSON
func parseJSON(content string) (*models.CaseJSON, error) {
	content = strings.TrimSpace(content)

	// Try to find JSON block if model added extra text
	if start := strings.Index(content, "{"); start != -1 {
		if end := strings.LastIndex(content, "}"); end != -1 && end >= start {
			content = content[start : end+1]
		}
	}

	var result models.CaseJSON
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("parse JSON from AI: %w\nContent: %s", err, content[:min(300, len(content))])
	}
	return &result, nil
}

// MergeChunks merges multiple CaseJSON results from chunked processing
// First chunk has the case metadata; all chunks contribute defendants and verdicts
func MergeChunks(chunks []*models.CaseJSON) *models.CaseJSON {
	if len(chunks) == 0 {
		return &models.CaseJSON{}
	}

	merged := *chunks[0] // First chunk has the header info

	// Deduplicate defendants by name
	defMap := make(map[string]models.Defendant)
	for _, chunk := range chunks {
		for _, d := range chunk.Defendants {
			if d.Name != "" {
				defMap[d.Name] = d
			}
		}
	}
	merged.Defendants = make([]models.Defendant, 0, len(defMap))
	for _, d := range defMap {
		merged.Defendants = append(merged.Defendants, d)
	}

	// Deduplicate verdicts by verdict text
	verdictMap := make(map[string]models.Verdict)
	for _, chunk := range chunks {
		for _, v := range chunk.Verdicts {
			if v.VerdictText != "" {
				verdictMap[v.VerdictText] = v
			}
		}
	}
	merged.Verdicts = make([]models.Verdict, 0, len(verdictMap))
	for _, v := range verdictMap {
		merged.Verdicts = append(merged.Verdicts, v)
	}

	return &merged
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
