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

const systemPrompt = `أنت وكيل نيابة مصري متخصص في تحليل ملفات القضايا الجنائية والإدارية.
تقوم بمراجعة الملفات المسحوبة ضوئياً (scanned documents) واستخراج بياناتها بدقة.

سياق العمل:
- الوثائق هي ملفات قضايا رسمية من النيابة العامة أو المحاكم المصرية
- الملفات تحتوي على: محاضر التحقيق، قرارات الاتهام، أحكام المحاكم
- المتهمون غالباً موظفون حكوميون أو من القطاع الخاص ارتكبوا مخالفات إدارية أو جنائية
- بعض الصفحات قد تكون غير واضحة بسبب جودة السحب الضوئي

مهمتك:
استخراج بيانات كل متهم ومخالفاته والحكم الصادر بحقه وإرجاعها بصيغة JSON فقط.
لا تضف أي نص أو شرح قبل JSON أو بعده.
إذا لم تجد معلومة ضع "" ولا تضع null أبداً.`

func buildExtractionPrompt(isChunk bool, chunkNum, totalChunks int) string {
	chunkNote := ""
	if isChunk {
		chunkNote = fmt.Sprintf(`

── تنبيه: معالجة مجزأة ──
هذا الجزء %d من أصل %d من نفس ملف القضية.
استخرج جميع المتهمين والمخالفات والأحكام الموجودة في هذه الصفحات فقط.
لا تفترض وجود بيانات في أجزاء أخرى.`, chunkNum, totalChunks)
	}

	return fmt.Sprintf(`أمامك صور صفحات من ملف قضية رسمية.
قم باستخراج البيانات التالية وأرجعها كـ JSON فقط:%s

─── هيكل JSON المطلوب ───
{
  "case_number": "رقم القضية كما هو مكتوب في الملف",
  "case_date": "تاريخ القضية أو صدور الحكم بصيغة YYYY-MM-DD، اتركه فارغاً إن لم يوجد",
  "court_name": "اسم المحكمة أو النيابة المختصة",
  "case_subject": "موضوع القضية أو وصف مختصر للمخالفات",
  "case_year": "سنة القضية كرقم فقط مثل 2023",
  "defendants": [
    {
      "name": "الاسم الرباعي للمتهم كما هو مكتوب",
      "national_id": "الرقم القومي المكون من 14 رقماً إن وجد، وإلا اتركه فارغاً",
      "job_title": "المسمى الوظيفي للمتهم",
      "workplace": "اسم الجهة أو المكان الذي يعمل به المتهم",
      "charges": [
        "المخالفة الأولى المنسوبة لهذا المتهم تحديداً",
        "المخالفة الثانية إن وجدت"
      ]
    }
  ],
  "verdicts": [
    {
      "defendant_names": [
        "اسم المتهم الأول المشمول بهذا الحكم",
        "اسم المتهم الثاني إن كان الحكم مشتركاً"
      ],
      "verdict_text": "نص الحكم الكامل كما ورد في الوثيقة",
      "verdict_type": "نوع الحكم: إدانة أو براءة أو حفظ أو إحالة أو تأجيل"
    }
  ]
}

─── تعليمات الاستخراج ───
المتهمون:
• ابحث عن الكلمات الدالة: "المتهم"، "المحال"، "المخالف"، "المنسوب إليه"
• كل شخص ورد بعد هذه الكلمات هو متهم يجب استخراج بياناته
• الرقم القومي يكون 14 رقماً متتالية
• الوظيفة وجهة العمل غالباً تأتي بعد اسم المتهم مباشرة

المخالفات:
• ابحث عن: "بتهمة"، "بمخالفة"، "بارتكابه"، "المنسوب إليه"، "اتهم بـ"
• كل مخالفة تُنسب لمتهم بعينه تُضاف في charges الخاصة به
• إذا كانت المخالفة مشتركة بين عدة متهمين أضفها لكل منهم

الأحكام:
• ابحث عن: "حكمت المحكمة"، "قررت النيابة"، "تقرر"، "يُعاقب"، "بالسجن"، "بالغرامة"، "بالبراءة"
• إذا كان حكم واحد يشمل عدة متهمين اذكر أسماءهم جميعاً في defendant_names
• أنقل نص الحكم كاملاً كما هو في الوثيقة

ملاحظات جودة الصور:
• إذا كانت الصورة غير واضحة حاول قراءة ما يمكن قراءته
• إذا كانت كلمة غير مقروءة استبدلها بـ "غير واضح"
• لا تخترع أو تفترض بيانات غير موجودة في الصور`, chunkNote)
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
