# Case Processor - نظام معالجة القضايا القانونية

نظام مبني بـ Go يقوم بـ:
1. جلب ملفات PDF للقضايا من MinIO
2. تحويل كل صفحة لصورة باستخدام `poppler`
3. إرسال الصور لنموذج Gemma4 على Ollama لاستخراج البيانات
4. حفظ بيانات المتهمين والأحكام في SQL Server

---

## متطلبات التشغيل

### برامج مطلوبة على السيرفر
```bash
# Go 1.22+
apt install golang-go

# Poppler (لتحويل PDF لصور)
apt install poppler-utils

# التحقق من التثبيت
pdftoppm -v
pdfinfo -v
# في حالة عدم وجود pdftoppm
قم بتحميل من الموقع
https://github.com/oschwartz10612/poppler-windows/releases
```

### خدمات مطلوبة
- **MinIO** - يعمل ومعاه الـ PDFs في bucket اسمه `Cases`
- **Ollama** - يعمل مع gemma4 محمل: `ollama pull gemma4`
- **SQL Server** - يعمل ومعاه database جاهزة

---

## التثبيت والإعداد

### 1. استنساخ المشروع
```bash
git clone <repo-url>
cd case-processor
```

### 2. إعداد ملف البيئة
```bash
cp .env.example .env
nano .env
```

عدّل القيم:
```env
MINIO_ENDPOINT=localhost:9000
MINIO_ACCESS_KEY=minioadmin
MINIO_SECRET_KEY=minioadmin
MINIO_BUCKET=Cases

OLLAMA_ENDPOINT=http://localhost:11434
OLLAMA_MODEL=gemma4
OLLAMA_MAX_PAGES_PER_CHUNK=30

DB_HOST=localhost
DB_PORT=1433
DB_NAME=CasesDB
DB_USER=sa
DB_PASSWORD=YourPassword123
```

### 3. تحميل الـ Dependencies
```bash
make deps
```

### 4. بناء البرنامج
```bash
make build
```

---

## التشغيل

### أول مرة - إنشاء الجداول
```bash
make run-init
# أو
./case-processor init-db
```

يُنشئ الجداول التالية:
- `cases` - بيانات القضايا الأساسية
- `case_defendants` - المتهمين وبياناتهم
- `case_verdicts` - الأحكام

### تشغيل دورة معالجة كاملة
```bash
make run-all
# أو
./case-processor run
```

### تشغيل كل خطوة منفردة
```bash
# 1. اكتشاف ملفات جديدة من MinIO
./case-processor discover

# 2. معالجة الملفات المسجلة
./case-processor process

# 3. إحصائيات
./case-processor stats
```

---

## هيكل قاعدة البيانات

### جدول cases
| عمود | النوع | الوصف |
|------|-------|-------|
| id | BIGINT | المعرف |
| file_name | NVARCHAR | اسم الملف (2233333.pdf) |
| case_number | NVARCHAR | رقم القضية |
| case_date | DATE | تاريخ القضية |
| court_name | NVARCHAR | اسم المحكمة |
| case_subject | NVARCHAR | موضوع القضية |
| raw_json | NVARCHAR(MAX) | JSON كامل من الـ AI |
| status | NVARCHAR | pending/processing/completed/needs_review/failed |
| retry_count | INT | عدد المحاولات |

### جدول case_defendants
| عمود | النوع | الوصف |
|------|-------|-------|
| case_id | BIGINT | FK للقضية |
| name | NVARCHAR | الاسم |
| national_id | NVARCHAR | الرقم القومي |
| job_title | NVARCHAR | الوظيفة |
| workplace | NVARCHAR | جهة العمل |
| charges | NVARCHAR | التهم (JSON array) |

### جدول case_verdicts
| عمود | النوع | الوصف |
|------|-------|-------|
| case_id | BIGINT | FK للقضية |
| defendant_names | NVARCHAR | أسماء المتهمين (JSON array) |
| verdict_text | NVARCHAR | نص الحكم |
| verdict_type | NVARCHAR | إدانة/براءة/تأجيل |

---

## الحالات (Status)

| الحالة | المعنى |
|--------|--------|
| `pending` | في الانتظار |
| `processing` | جاري المعالجة |
| `completed` | اكتملت بنجاح |
| `failed` | فشلت، ستُعاد المحاولة |
| `needs_review` | فشلت مرتين، تحتاج مراجعة يدوية |

---

## استعلامات مفيدة

```sql
-- القضايا اللي محتاجة مراجعة
SELECT file_name, error_msg FROM cases WHERE status = 'needs_review';

-- إحصائية بالحالات
SELECT status, COUNT(*) FROM cases GROUP BY status;

-- متهم معين وقضاياه
SELECT c.case_number, d.name, d.job_title, d.charges
FROM case_defendants d
JOIN cases c ON c.id = d.case_id
WHERE d.name LIKE N'%أحمد%';

-- إعادة معالجة قضية معينة
UPDATE cases SET status='pending', retry_count=0 WHERE file_name='2233333.pdf';
```

---

## إعدادات الأداء

في `.env` يمكن ضبط:

```env
# عدد القضايا المتوازية (زود لو الموارد كافية)
CONCURRENT_WORKERS=2

# صفحات لكل chunk (زود لو الموديل يتحمل)
OLLAMA_MAX_PAGES_PER_CHUNK=30

# جودة الصور (150=سريع، 200=متوازن، 300=جودة عالية)
PDF_DPI=200
```
