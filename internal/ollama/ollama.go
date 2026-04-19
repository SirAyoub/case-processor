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
	endpoint       string
	model          string
	http           *http.Client
	timeoutPerPage time.Duration
}

func New(endpoint, model string) *Client {
	return NewWithTimeout(endpoint, model, 3*time.Minute)
}

func NewWithTimeout(endpoint, model string, timeoutPerPage time.Duration) *Client {
	return &Client{
		endpoint:       endpoint,
		model:          model,
		http:           &http.Client{Timeout: 0}, // no global timeout — handled per-request
		timeoutPerPage: timeoutPerPage,
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

// ExtractCaseData sends pages to Ollama with retry + dynamic timeout.
// Timeout = 5 min base + 3 min × number of pages.
func (c *Client) ExtractCaseData(ctx context.Context, pages []pdf.PageImage, chunkNum, totalChunks int) (*models.CaseJSON, error) {
	const maxAttempts = 3

	// Dynamic timeout based on page count
	// 30 pages → 5 + 90 = 95 minutes
	dynamicTimeout := 5*time.Minute + time.Duration(len(pages))*c.timeoutPerPage

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			// Exponential backoff: 30s, 120s
			backoff := time.Duration(attempt*attempt) * 30 * time.Second
			fmt.Printf("[ollama] chunk %d: waiting %v before retry %d/%d\n", chunkNum, backoff, attempt, maxAttempts)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		reqCtx, cancel := context.WithTimeout(ctx, dynamicTimeout)
		fmt.Printf("[ollama] chunk %d/%d: attempt %d, pages=%d, timeout=%v\n",
			chunkNum, totalChunks, attempt, len(pages), dynamicTimeout)

		result, err := c.doRequest(reqCtx, pages, chunkNum, totalChunks)
		cancel()

		if err == nil {
			return result, nil
		}

		lastErr = err
		fmt.Printf("[ollama] chunk %d attempt %d failed: %v\n", chunkNum, attempt, err)
	}

	return nil, fmt.Errorf("all %d attempts failed, last: %w", maxAttempts, lastErr)
}

func (c *Client) doRequest(ctx context.Context, pages []pdf.PageImage, chunkNum, totalChunks int) (*models.CaseJSON, error) {
	images := make([]string, len(pages))
	for i, p := range pages {
		images[i] = p.Base64
	}

	req := ollamaRequest{
		Model:  c.model,
		Stream: false,
		Options: map[string]any{
			"temperature": 0.1,
			"num_ctx":     32768,
		},
		Messages: []ollamaMessage{
			{Role: "system", Content: systemPrompt},
			{
				Role:    "user",
				Content: buildExtractionPrompt(totalChunks > 1, chunkNum, totalChunks),
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
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b[:min(200, len(b))]))
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var ollamaResp ollamaResponse
	if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w\nBody: %s", err, string(respBody[:min(200, len(respBody))]))
	}

	if ollamaResp.Error != "" {
		return nil, fmt.Errorf("ollama error: %s", ollamaResp.Error)
	}

	return parseJSON(ollamaResp.Message.Content)
}

// ─── JSON Parsing ─────────────────────────────────────────────────────────────

func parseJSON(content string) (*models.CaseJSON, error) {
	content = strings.TrimSpace(content)

	// Strip ```json ... ``` fences if model added them
	if strings.Contains(content, "```") {
		start := strings.Index(content, "\n")
		end := strings.LastIndex(content, "```")
		if start != -1 && end > start {
			content = strings.TrimSpace(content[start+1 : end])
		}
	}

	// Find outermost { ... }
	if s := strings.Index(content, "{"); s != -1 {
		if e := strings.LastIndex(content, "}"); e >= s {
			content = content[s : e+1]
		}
	}

	var result models.CaseJSON
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		preview := content
		if len(preview) > 400 {
			preview = preview[:400]
		}
		return nil, fmt.Errorf("parse JSON: %w\nPreview: %s", err, preview)
	}
	return &result, nil
}

// ─── Chunk Merging ────────────────────────────────────────────────────────────

func MergeChunks(chunks []*models.CaseJSON) *models.CaseJSON {
	if len(chunks) == 0 {
		return &models.CaseJSON{}
	}
	merged := *chunks[0]

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
