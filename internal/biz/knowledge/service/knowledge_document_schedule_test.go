package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-base-agent/internal/biz/crawler"
	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
	knowledgeRepo "go-base-agent/internal/biz/knowledge/repo"
	"go-base-agent/internal/framework/config"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeScheduleSource struct {
	name      string
	fetchedID string
	doc       *crawler.Document
	onFetch   func()
}

func (f *fakeScheduleSource) Name() string {
	if f.name != "" {
		return f.name
	}
	return "url"
}

func (f *fakeScheduleSource) ListDocuments(context.Context) ([]crawler.DocumentMeta, error) {
	return nil, nil
}

func (f *fakeScheduleSource) FetchDocument(_ context.Context, id string) (*crawler.Document, error) {
	f.fetchedID = id
	if f.onFetch != nil {
		f.onFetch()
	}
	return f.doc, nil
}

func (f *fakeScheduleSource) WatchChanges(context.Context, time.Time) (<-chan crawler.ChangeEvent, error) {
	ch := make(chan crawler.ChangeEvent)
	close(ch)
	return ch, nil
}

type fakeScheduleGeelibSource struct {
	fakeScheduleSource
	fetchedTreeURL string
	docs           []crawler.Document
}

func (f *fakeScheduleGeelibSource) Name() string {
	return "internal_url"
}

func (f *fakeScheduleGeelibSource) FetchDocuments(_ context.Context, rawURL string) ([]crawler.Document, error) {
	f.fetchedTreeURL = rawURL
	return f.docs, nil
}

type fakeScheduleFileStore struct {
	docID string
	name  string
	data  []byte
}

func (f *fakeScheduleFileStore) Put(docID string, name string, data []byte) {
	f.docID = docID
	f.name = name
	f.data = append([]byte(nil), data...)
}

type fakeScheduleCollectionFileStore struct {
	files    map[string][]byte
	readErrs map[string]error
	puts     []string
}

func (f *fakeScheduleCollectionFileStore) Put(docID string, name string, data []byte) {
	_ = f.PutWithCollection(context.Background(), "", docID, name, data)
}

func (f *fakeScheduleCollectionFileStore) PutWithCollection(_ context.Context, collectionName, docID, name string, data []byte) error {
	if f.files == nil {
		f.files = make(map[string][]byte)
	}
	key := collectionName + "/" + docID
	f.files[key] = append([]byte(nil), data...)
	f.puts = append(f.puts, docID+":"+name)
	return nil
}

func (f *fakeScheduleCollectionFileStore) ReadWithCollection(_ context.Context, collectionName, docID string) ([]byte, error) {
	if f.files == nil {
		return nil, nil
	}
	key := collectionName + "/" + docID
	if err := f.readErrs[key]; err != nil {
		return nil, err
	}
	return append([]byte(nil), f.files[key]...), nil
}

type fakeScheduleChunkStarter struct {
	docID  string
	userID string
}

func (f *fakeScheduleChunkStarter) StartChunk(_ context.Context, docID string, userID string) error {
	f.docID = docID
	f.userID = userID
	return nil
}

type fakeScheduleChunkRunner struct {
	docID     string
	userID    string
	runErr    error
	runCalled bool
	runDocIDs []string
}

func (f *fakeScheduleChunkRunner) StartChunk(_ context.Context, docID string, userID string) error {
	f.docID = docID
	f.userID = userID
	return nil
}

func (f *fakeScheduleChunkRunner) RunChunkNow(_ context.Context, docID string, userID string) error {
	f.runCalled = true
	f.docID = docID
	f.userID = userID
	f.runDocIDs = append(f.runDocIDs, docID)
	return f.runErr
}

func TestDocumentScheduleService_ScanDueSyncsInternalURLTree(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate schedule tables: %v", err)
	}

	now := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	parentURL := "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=368231"
	childAURL := "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=437090"
	childBURL := "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=439113"
	childCURL := "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=439175"
	kb := &knowledgeModel.KnowledgeBase{Name: "kb", CollectionName: "kb_collection", EmbeddingModel: "emb", CreatedBy: "tester"}
	kb.ID = "kb-1"
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	docA := &knowledgeModel.KnowledgeDocument{
		KbID:            kb.ID,
		DocName:         "游客模式.md",
		FileURL:         childAURL,
		FileType:        "md",
		SourceType:      "internal_url",
		SourceLocation:  parentURL,
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "success",
		CreatedBy:       "user-1",
	}
	docA.ID = "doc-a"
	docB := &knowledgeModel.KnowledgeDocument{
		KbID:           kb.ID,
		DocName:        "旧子文档.md",
		FileURL:        childBURL,
		FileType:       "md",
		SourceType:     "internal_url",
		SourceLocation: parentURL,
		Status:         "success",
		CreatedBy:      "user-1",
	}
	docB.ID = "doc-b"
	if err := gdb.Create(docA).Error; err != nil {
		t.Fatalf("seed doc a: %v", err)
	}
	if err := gdb.Create(docB).Error; err != nil {
		t.Fatalf("seed doc b: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:       docA.ID,
		KbID:        kb.ID,
		CronExpr:    "@every 1h",
		Enabled:     1,
		NextRunTime: ptrTime(now.Add(-time.Minute)),
	}
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	fileStore := &fakeScheduleCollectionFileStore{files: map[string][]byte{
		kb.CollectionName + "/" + docA.ID: []byte("old content"),
		kb.CollectionName + "/" + docB.ID: []byte("removed content"),
	}}
	chunkStarter := &fakeScheduleChunkRunner{}
	source := &fakeScheduleGeelibSource{
		docs: []crawler.Document{
			{
				Meta:    crawler.DocumentMeta{ID: "437090", Title: "游客模式.md", URL: childAURL, MimeType: "text/markdown", SourceName: "geelib"},
				Content: []byte("new content"),
			},
			{
				Meta:    crawler.DocumentMeta{ID: "439175", Title: "新增子文档.md", URL: childCURL, MimeType: "text/markdown", SourceName: "geelib"},
				Content: []byte("created content"),
			},
		},
	}

	svc := NewDocumentScheduleService(
		gdb,
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		fileStore,
		chunkStarter,
		config.RAGKnowledgeScheduleConfig{BatchSize: 10, LockSeconds: 60},
	)
	svc.now = func() time.Time { return now }
	svc.RegisterSource(source)

	count, err := svc.ScanDue(context.Background())
	if err != nil {
		t.Fatalf("scan due: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 processed schedule, got %d", count)
	}
	if source.fetchedTreeURL != parentURL {
		t.Fatalf("expected tree fetch %q, got %q", parentURL, source.fetchedTreeURL)
	}

	var updatedA knowledgeModel.KnowledgeDocument
	if err := gdb.First(&updatedA, "id = ?", docA.ID).Error; err != nil {
		t.Fatalf("find doc a: %v", err)
	}
	if updatedA.Enabled != 1 || updatedA.FileURL != childAURL || updatedA.SourceLocation != parentURL {
		t.Fatalf("doc a not preserved as active child: %+v", updatedA)
	}
	var disabledB knowledgeModel.KnowledgeDocument
	if err := gdb.First(&disabledB, "id = ?", docB.ID).Error; err != nil {
		t.Fatalf("find doc b: %v", err)
	}
	if disabledB.Enabled != 0 {
		t.Fatalf("removed child should be disabled, got %+v", disabledB)
	}
	var createdC knowledgeModel.KnowledgeDocument
	if err := gdb.First(&createdC, "file_url = ?", childCURL).Error; err != nil {
		t.Fatalf("find created child: %v", err)
	}
	if createdC.KbID != kb.ID || createdC.SourceLocation != parentURL || createdC.SourceType != "internal_url" || createdC.Enabled != 1 {
		t.Fatalf("created child has unexpected fields: %+v", createdC)
	}
	if len(chunkStarter.runDocIDs) != 2 || chunkStarter.runDocIDs[0] != docA.ID || chunkStarter.runDocIDs[1] != createdC.ID {
		t.Fatalf("expected changed and created docs to be chunked, got %v", chunkStarter.runDocIDs)
	}
	if string(fileStore.files[kb.CollectionName+"/"+docA.ID]) != "new content" {
		t.Fatalf("doc a file not updated: %q", string(fileStore.files[kb.CollectionName+"/"+docA.ID]))
	}
	if string(fileStore.files[kb.CollectionName+"/"+createdC.ID]) != "created content" {
		t.Fatalf("doc c file not saved: %q", string(fileStore.files[kb.CollectionName+"/"+createdC.ID]))
	}

	var updatedSchedule knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&updatedSchedule, "doc_id = ?", docA.ID).Error; err != nil {
		t.Fatalf("find updated schedule: %v", err)
	}
	if updatedSchedule.LastStatus != "success" || updatedSchedule.LastContentHash == "" || updatedSchedule.NextRunTime == nil {
		t.Fatalf("schedule not marked success: %+v", updatedSchedule)
	}
}

func TestDocumentScheduleService_InternalURLTreeRefreshReusesCanonicalChild(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate schedule tables: %v", err)
	}

	now := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	sURL := "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=s"
	aURL := "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=a"
	kb := &knowledgeModel.KnowledgeBase{Name: "kb", CollectionName: "kb_collection", EmbeddingModel: "emb", CreatedBy: "tester"}
	kb.ID = "kb-canonical"
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	docS := &knowledgeModel.KnowledgeDocument{
		KbID:               kb.ID,
		DocName:            "S.md",
		FileURL:            sURL,
		FileType:           "md",
		SourceType:         "internal_url",
		SourceLocation:     sURL,
		CanonicalSourceKey: InternalURLCanonicalSourceKey(sURL),
		SourceRootKey:      InternalURLCanonicalSourceKey(sURL),
		ScheduleEnabled:    1,
		ScheduleCron:       "@every 1h",
		Status:             "success",
		CreatedBy:          "user-1",
	}
	docS.ID = "doc-s"
	docA := &knowledgeModel.KnowledgeDocument{
		KbID:               kb.ID,
		DocName:            "A.md",
		FileURL:            aURL,
		FileType:           "md",
		SourceType:         "internal_url",
		SourceLocation:     aURL,
		CanonicalSourceKey: InternalURLCanonicalSourceKey(aURL),
		SourceRootKey:      InternalURLCanonicalSourceKey(aURL),
		Status:             "success",
		CreatedBy:          "user-1",
	}
	docA.ID = "doc-a"
	if err := gdb.Create(docS).Error; err != nil {
		t.Fatalf("seed doc s: %v", err)
	}
	if err := gdb.Create(docA).Error; err != nil {
		t.Fatalf("seed doc a: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:       docS.ID,
		KbID:        kb.ID,
		CronExpr:    "@every 1h",
		Enabled:     1,
		NextRunTime: ptrTime(now.Add(-time.Minute)),
	}
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	fileStore := &fakeScheduleCollectionFileStore{files: map[string][]byte{
		kb.CollectionName + "/" + docS.ID: []byte("# S old"),
		kb.CollectionName + "/" + docA.ID: []byte("# A old"),
	}}
	chunkStarter := &fakeScheduleChunkRunner{}
	source := &fakeScheduleGeelibSource{
		docs: []crawler.Document{
			{Meta: crawler.DocumentMeta{ID: "s", Title: "S.md", URL: sURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# S")},
			{Meta: crawler.DocumentMeta{ID: "a", Title: "A.md", URL: aURL, MimeType: "text/markdown", SourceName: "geelib", Extra: map[string]string{"parent_url": sURL}}, Content: []byte("# A")},
		},
	}
	svc := NewDocumentScheduleService(
		gdb,
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		fileStore,
		chunkStarter,
		config.RAGKnowledgeScheduleConfig{BatchSize: 10, LockSeconds: 60},
	)
	svc.now = func() time.Time { return now }
	svc.RegisterSource(source)

	count, err := svc.ScanDue(context.Background())
	if err != nil {
		t.Fatalf("scan due: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 processed schedule, got %d", count)
	}
	var docCount int64
	if err := gdb.Model(&knowledgeModel.KnowledgeDocument{}).Count(&docCount).Error; err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if docCount != 2 {
		t.Fatalf("expected canonical child reuse and 2 docs total, got %d", docCount)
	}
	var updatedA knowledgeModel.KnowledgeDocument
	if err := gdb.First(&updatedA, "id = ?", docA.ID).Error; err != nil {
		t.Fatalf("find updated a: %v", err)
	}
	if updatedA.SourceRootKey != InternalURLCanonicalSourceKey(sURL) {
		t.Fatalf("expected a to be reassigned under s root, got %q", updatedA.SourceRootKey)
	}
	if updatedA.SourceParentKey != InternalURLCanonicalSourceKey(sURL) {
		t.Fatalf("expected a parent to be s, got %q", updatedA.SourceParentKey)
	}
}

func TestDocumentScheduleService_ScanDueKeepsManuallyDisabledInternalURLChildDisabled(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate schedule tables: %v", err)
	}

	now := time.Date(2026, 8, 7, 11, 0, 0, 0, time.UTC)
	parentURL := "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=368231"
	childURL := "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=437090"
	kb := &knowledgeModel.KnowledgeBase{Name: "kb", CollectionName: "kb_collection", EmbeddingModel: "emb", CreatedBy: "tester"}
	kb.ID = "kb-disabled"
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            kb.ID,
		DocName:         "手动停用.md",
		Enabled:         0,
		FileURL:         childURL,
		FileType:        "md",
		SourceType:      "internal_url",
		SourceLocation:  parentURL,
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "success",
		CreatedBy:       "user-1",
	}
	doc.ID = "doc-disabled"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	if err := gdb.Model(&knowledgeModel.KnowledgeDocument{}).Where("id = ?", doc.ID).Update("enabled", int16(0)).Error; err != nil {
		t.Fatalf("disable doc: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:       doc.ID,
		KbID:        kb.ID,
		CronExpr:    "@every 1h",
		Enabled:     1,
		NextRunTime: ptrTime(now.Add(-time.Minute)),
	}
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	fileStore := &fakeScheduleCollectionFileStore{files: map[string][]byte{
		kb.CollectionName + "/" + doc.ID: []byte("old content"),
	}}
	source := &fakeScheduleGeelibSource{docs: []crawler.Document{{
		Meta:    crawler.DocumentMeta{ID: "437090", Title: "手动停用.md", URL: childURL, MimeType: "text/markdown", SourceName: "geelib"},
		Content: []byte("new content"),
	}}}
	chunkStarter := &fakeScheduleChunkRunner{}
	svc := NewDocumentScheduleService(
		gdb,
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		fileStore,
		chunkStarter,
		config.RAGKnowledgeScheduleConfig{BatchSize: 10, LockSeconds: 60},
	)
	svc.now = func() time.Time { return now }
	svc.RegisterSource(source)

	if _, err := svc.ScanDue(context.Background()); err != nil {
		t.Fatalf("scan due: %v", err)
	}
	var updated knowledgeModel.KnowledgeDocument
	if err := gdb.First(&updated, "id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find updated doc: %v", err)
	}
	if updated.Enabled != 0 {
		t.Fatalf("manually disabled child should stay disabled, got %+v", updated)
	}
}

func TestDocumentScheduleService_ScanDueRebuildsInternalURLChildWhenStoredFileMissing(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate schedule tables: %v", err)
	}

	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	parentURL := "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=368231"
	childURL := "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=437090"
	kb := &knowledgeModel.KnowledgeBase{Name: "kb", CollectionName: "kb_collection", EmbeddingModel: "emb", CreatedBy: "tester"}
	kb.ID = "kb-missing-file"
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            kb.ID,
		DocName:         "源文件缺失.md",
		Enabled:         1,
		FileURL:         childURL,
		FileType:        "md",
		SourceType:      "internal_url",
		SourceLocation:  parentURL,
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "success",
		CreatedBy:       "user-1",
	}
	doc.ID = "doc-missing-file"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:       doc.ID,
		KbID:        kb.ID,
		CronExpr:    "@every 1h",
		Enabled:     1,
		NextRunTime: ptrTime(now.Add(-time.Minute)),
	}
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	key := kb.CollectionName + "/" + doc.ID
	fileStore := &fakeScheduleCollectionFileStore{
		files:    map[string][]byte{},
		readErrs: map[string]error{key: errors.New("object not found")},
	}
	source := &fakeScheduleGeelibSource{docs: []crawler.Document{{
		Meta:    crawler.DocumentMeta{ID: "437090", Title: "源文件缺失.md", URL: childURL, MimeType: "text/markdown", SourceName: "geelib"},
		Content: []byte("rebuilt content"),
	}}}
	chunkStarter := &fakeScheduleChunkRunner{}
	svc := NewDocumentScheduleService(
		gdb,
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		fileStore,
		chunkStarter,
		config.RAGKnowledgeScheduleConfig{BatchSize: 10, LockSeconds: 60},
	)
	svc.now = func() time.Time { return now }
	svc.RegisterSource(source)

	if _, err := svc.ScanDue(context.Background()); err != nil {
		t.Fatalf("scan due: %v", err)
	}
	if string(fileStore.files[key]) != "rebuilt content" {
		t.Fatalf("missing stored file should be rebuilt, got %q", string(fileStore.files[key]))
	}
	if len(chunkStarter.runDocIDs) != 1 || chunkStarter.runDocIDs[0] != doc.ID {
		t.Fatalf("expected rebuilt document to be chunked, got %v", chunkStarter.runDocIDs)
	}
	var updatedSchedule knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&updatedSchedule, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find updated schedule: %v", err)
	}
	if updatedSchedule.LastStatus != "success" {
		t.Fatalf("schedule should succeed after rebuilding missing file, got %+v", updatedSchedule)
	}
}

func TestDocumentScheduleService_ScanDueFetchesRemoteDocumentAndStartsChunk(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lock-heartbeat.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate schedule tables: %v", err)
	}

	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            "kb-1",
		DocName:         "old.md",
		FileURL:         "https://example.com/member-agent.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/member-agent.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "success",
		CreatedBy:       "user-1",
	}
	doc.ID = "doc-1"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:       doc.ID,
		KbID:        doc.KbID,
		CronExpr:    "@every 1h",
		Enabled:     1,
		NextRunTime: ptrTime(now.Add(-time.Minute)),
	}
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	fileStore := &fakeScheduleFileStore{}
	chunkStarter := &fakeScheduleChunkStarter{}
	source := &fakeScheduleSource{doc: &crawler.Document{
		Meta: crawler.DocumentMeta{
			ID:         doc.SourceLocation,
			Title:      "member-agent.md",
			URL:        doc.SourceLocation,
			MimeType:   "text/markdown",
			Size:       34,
			SourceName: "url",
			UpdatedAt:  now,
			Extra:      map[string]string{"etag": "etag-1", "last_modified": "Thu, 16 Jul 2026 10:00:00 GMT"},
		},
		Content: []byte("会员 Agent 支持权益查询和积分查询。"),
	}}

	svc := NewDocumentScheduleService(
		gdb,
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		fileStore,
		chunkStarter,
		config.RAGKnowledgeScheduleConfig{BatchSize: 10, LockSeconds: 60},
	)
	svc.now = func() time.Time { return now }
	svc.RegisterSource(source)

	count, err := svc.ScanDue(context.Background())
	if err != nil {
		t.Fatalf("scan due: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 processed schedule, got %d", count)
	}
	if source.fetchedID != doc.SourceLocation {
		t.Fatalf("expected fetch %q, got %q", doc.SourceLocation, source.fetchedID)
	}
	if fileStore.docID != doc.ID || fileStore.name != "member-agent.md" || string(fileStore.data) != string(source.doc.Content) {
		t.Fatalf("unexpected stored file: doc=%s name=%s data=%s", fileStore.docID, fileStore.name, string(fileStore.data))
	}
	if chunkStarter.docID != doc.ID || chunkStarter.userID != doc.CreatedBy {
		t.Fatalf("expected chunk start doc/user, got %s/%s", chunkStarter.docID, chunkStarter.userID)
	}

	var updated knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&updated, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find updated schedule: %v", err)
	}
	if updated.LastStatus != "success" || updated.LastSuccessTime == nil || updated.LastContentHash == "" || updated.NextRunTime == nil {
		t.Fatalf("schedule not marked success: %+v", updated)
	}
	if !updated.NextRunTime.After(now) {
		t.Fatalf("expected next run after now, got %v", updated.NextRunTime)
	}

	var execCount int64
	if err := gdb.Model(&knowledgeModel.KnowledgeDocumentScheduleExec{}).Where("doc_id = ? AND status = ?", doc.ID, "success").Count(&execCount).Error; err != nil {
		t.Fatalf("count schedule exec: %v", err)
	}
	if execCount != 1 {
		t.Fatalf("expected one success exec log, got %d", execCount)
	}
}

func TestDocumentScheduleService_ScanDueKeepsLockAliveDuringLongRefresh(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lock-heartbeat.db")
	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate schedule tables: %v", err)
	}

	baseNow := time.Now()
	release := make(chan struct{})
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            "kb-1",
		DocName:         "slow.md",
		FileURL:         "https://example.com/slow.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/slow.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "success",
		CreatedBy:       "user-1",
	}
	doc.ID = "doc-slow"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:       doc.ID,
		KbID:        doc.KbID,
		CronExpr:    "@every 1h",
		Enabled:     1,
		NextRunTime: ptrTime(baseNow.Add(-time.Minute)),
	}
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	fileStore := &fakeScheduleFileStore{}
	chunkStarter := &fakeScheduleChunkRunner{}
	source := &fakeScheduleSource{
		doc: &crawler.Document{
			Meta: crawler.DocumentMeta{
				ID:         doc.SourceLocation,
				Title:      "slow.md",
				URL:        doc.SourceLocation,
				MimeType:   "text/markdown",
				Size:       48,
				SourceName: "url",
				UpdatedAt:  baseNow,
				Extra:      map[string]string{"etag": "etag-slow", "last_modified": "Thu, 16 Jul 2026 10:00:00 GMT"},
			},
			Content: []byte("会员 Agent 支持权益查询和积分查询，且需要长时间处理。"),
		},
		onFetch: func() {
			<-release
		},
	}

	svc := NewDocumentScheduleService(
		gdb,
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		fileStore,
		chunkStarter,
		config.RAGKnowledgeScheduleConfig{BatchSize: 10, LockSeconds: 1},
	)
	var renewCount int
	var lastRenew time.Time
	svc.lockRenewObserver = func(_ string, lockUntil time.Time) {
		renewCount++
		lastRenew = lockUntil
	}
	svc.RegisterSource(source)

	done := make(chan struct{})
	var scanCount int
	var scanErr error
	go func() {
		scanCount, scanErr = svc.ScanDue(context.Background())
		close(done)
	}()

	time.Sleep(1500 * time.Millisecond)
	if renewCount == 0 || !lastRenew.After(baseNow.Add(1500*time.Millisecond)) {
		t.Fatalf("expected heartbeat to renew lock, count=%d lastRenew=%v", renewCount, lastRenew)
	}

	close(release)
	<-done
	if scanErr != nil {
		t.Fatalf("scan due: %v", scanErr)
	}
	if scanCount != 1 {
		t.Fatalf("expected 1 processed schedule, got %d", scanCount)
	}

	var updated knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&updated, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find updated schedule: %v", err)
	}
	if updated.LastStatus != "success" || updated.LastSuccessTime == nil {
		t.Fatalf("expected success state to be written with active lock, got %+v", updated)
	}
	if updated.LockOwner != "" || updated.LockUntil != nil {
		t.Fatalf("expected lock released after success, got %+v", updated)
	}
}

func TestDocumentScheduleService_ScanDueMarksSkippedWhenRemoteDocumentUnchanged(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate schedule tables: %v", err)
	}

	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	content := []byte("unchanged content")
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            "kb-1",
		DocName:         "same.md",
		FileURL:         "https://example.com/same.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/same.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "success",
		CreatedBy:       "user-1",
	}
	doc.ID = "doc-unchanged"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:           doc.ID,
		KbID:            doc.KbID,
		CronExpr:        "@every 1h",
		Enabled:         1,
		LastContentHash: sha256Hex(content),
		NextRunTime:     ptrTime(now.Add(-time.Minute)),
	}
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	fileStore := &fakeScheduleFileStore{}
	chunkStarter := &fakeScheduleChunkStarter{}
	source := &fakeScheduleSource{doc: &crawler.Document{
		Meta: crawler.DocumentMeta{
			Title:    "same.md",
			URL:      doc.SourceLocation,
			MimeType: "text/markdown",
			Extra:    map[string]string{"etag": "etag-2", "last_modified": "Thu, 16 Jul 2026 10:00:00 GMT"},
		},
		Content: content,
	}}
	svc := NewDocumentScheduleService(
		gdb,
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		fileStore,
		chunkStarter,
		config.RAGKnowledgeScheduleConfig{BatchSize: 10, LockSeconds: 60},
	)
	svc.now = func() time.Time { return now }
	svc.RegisterSource(source)

	count, err := svc.ScanDue(context.Background())
	if err != nil {
		t.Fatalf("scan due: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 processed schedule, got %d", count)
	}
	if fileStore.docID != "" || chunkStarter.docID != "" {
		t.Fatalf("unchanged document should not be stored or chunked, stored=%s chunked=%s", fileStore.docID, chunkStarter.docID)
	}

	var updated knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&updated, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find updated schedule: %v", err)
	}
	if updated.LastStatus != "skipped" || updated.LastSuccessTime != nil || updated.NextRunTime == nil {
		t.Fatalf("schedule not marked skipped: %+v", updated)
	}
	if updated.LastETag != "etag-2" || updated.LastModified == "" {
		t.Fatalf("schedule did not retain remote validators: %+v", updated)
	}

	var exec knowledgeModel.KnowledgeDocumentScheduleExec
	if err := gdb.First(&exec, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find schedule exec: %v", err)
	}
	if exec.Status != "skipped" || exec.ContentHash != sha256Hex(content) || exec.EndTime == nil {
		t.Fatalf("exec not marked skipped: %+v", exec)
	}
}

func TestDocumentScheduleService_ScanDueSkipsWhenDocumentIsRunning(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate schedule tables: %v", err)
	}

	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            "kb-1",
		DocName:         "running.md",
		FileURL:         "https://example.com/running.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/running.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "running",
		CreatedBy:       "user-1",
	}
	doc.ID = "doc-running"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:       doc.ID,
		KbID:        doc.KbID,
		CronExpr:    "@every 1h",
		Enabled:     1,
		NextRunTime: ptrTime(now.Add(-time.Minute)),
	}
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	fileStore := &fakeScheduleFileStore{}
	chunkStarter := &fakeScheduleChunkStarter{}
	source := &fakeScheduleSource{doc: &crawler.Document{
		Meta: crawler.DocumentMeta{
			Title:    "running.md",
			URL:      doc.SourceLocation,
			MimeType: "text/markdown",
			Extra:    map[string]string{"etag": "etag-running"},
		},
		Content: []byte("new remote content"),
	}}
	svc := NewDocumentScheduleService(
		gdb,
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		fileStore,
		chunkStarter,
		config.RAGKnowledgeScheduleConfig{BatchSize: 10, LockSeconds: 60},
	)
	svc.now = func() time.Time { return now }
	svc.RegisterSource(source)

	count, err := svc.ScanDue(context.Background())
	if err != nil {
		t.Fatalf("scan due: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 processed schedule, got %d", count)
	}
	if fileStore.docID != "" || chunkStarter.docID != "" {
		t.Fatalf("running document should not be stored or chunked, stored=%s chunked=%s", fileStore.docID, chunkStarter.docID)
	}

	var updated knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&updated, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find updated schedule: %v", err)
	}
	if updated.LastStatus != "skipped" || updated.LastError == "" || updated.NextRunTime == nil {
		t.Fatalf("schedule not marked skipped: %+v", updated)
	}
	var exec knowledgeModel.KnowledgeDocumentScheduleExec
	if err := gdb.First(&exec, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find schedule exec: %v", err)
	}
	if exec.Status != "skipped" || exec.Message == "" || exec.EndTime == nil {
		t.Fatalf("exec not marked skipped: %+v", exec)
	}
}

func TestDocumentScheduleService_ScanDueDoesNotOverwriteScheduleWhenLeaseLost(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate schedule tables: %v", err)
	}

	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            "kb-1",
		DocName:         "lease.md",
		FileURL:         "https://example.com/lease.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/lease.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "success",
		CreatedBy:       "user-1",
	}
	doc.ID = "doc-lease"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:       doc.ID,
		KbID:        doc.KbID,
		CronExpr:    "@every 1h",
		Enabled:     1,
		NextRunTime: ptrTime(now.Add(-time.Minute)),
	}
	schedule.ID = "schedule-lease"
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	source := &fakeScheduleSource{
		doc: &crawler.Document{
			Meta: crawler.DocumentMeta{
				Title:    "lease.md",
				URL:      doc.SourceLocation,
				MimeType: "text/markdown",
				Extra:    map[string]string{"etag": "etag-lease"},
			},
			Content: []byte("new lease content"),
		},
		onFetch: func() {
			if err := gdb.Model(&knowledgeModel.KnowledgeDocumentSchedule{}).
				Where("id = ?", schedule.ID).
				Update("lock_owner", "other-worker").Error; err != nil {
				t.Fatalf("steal lock: %v", err)
			}
		},
	}
	svc := NewDocumentScheduleService(
		gdb,
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		&fakeScheduleFileStore{},
		&fakeScheduleChunkStarter{},
		config.RAGKnowledgeScheduleConfig{BatchSize: 10, LockSeconds: 60},
	)
	svc.now = func() time.Time { return now }
	svc.RegisterSource(source)

	count, err := svc.ScanDue(context.Background())
	if err != nil {
		t.Fatalf("scan due: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 processed schedule, got %d", count)
	}

	var updated knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&updated, "id = ?", schedule.ID).Error; err != nil {
		t.Fatalf("find updated schedule: %v", err)
	}
	if updated.LockOwner != "other-worker" || updated.LastStatus != "" || updated.LastSuccessTime != nil {
		t.Fatalf("stale worker overwrote schedule: %+v", updated)
	}
	var exec knowledgeModel.KnowledgeDocumentScheduleExec
	if err := gdb.First(&exec, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find schedule exec: %v", err)
	}
	if exec.Status != "success" || !strings.Contains(exec.Message, scheduleLeaseLostNote) {
		t.Fatalf("exec should retain lease lost note: %+v", exec)
	}
}

func TestDocumentScheduleService_ScanDueUsesSynchronousChunkRunnerWhenAvailable(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate schedule tables: %v", err)
	}

	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            "kb-1",
		DocName:         "sync.md",
		FileURL:         "https://example.com/sync.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/sync.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "success",
		CreatedBy:       "user-1",
	}
	doc.ID = "doc-sync"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:       doc.ID,
		KbID:        doc.KbID,
		CronExpr:    "@every 1h",
		Enabled:     1,
		NextRunTime: ptrTime(now.Add(-time.Minute)),
	}
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	fileStore := &fakeScheduleFileStore{}
	runner := &fakeScheduleChunkRunner{runErr: context.DeadlineExceeded}
	source := &fakeScheduleSource{doc: &crawler.Document{
		Meta: crawler.DocumentMeta{
			Title:    "sync.md",
			URL:      doc.SourceLocation,
			MimeType: "text/markdown",
			Extra:    map[string]string{"etag": "etag-sync"},
		},
		Content: []byte("sync content"),
	}}
	svc := NewDocumentScheduleService(
		gdb,
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		fileStore,
		runner,
		config.RAGKnowledgeScheduleConfig{BatchSize: 10, LockSeconds: 60},
	)
	svc.now = func() time.Time { return now }
	svc.RegisterSource(source)

	count, err := svc.ScanDue(context.Background())
	if err != nil {
		t.Fatalf("scan due: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 processed schedule, got %d", count)
	}
	if !runner.runCalled {
		t.Fatalf("expected synchronous chunk runner to be called")
	}
	if fileStore.docID != doc.ID {
		t.Fatalf("expected file to be stored before chunking, got %s", fileStore.docID)
	}

	var updated knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&updated, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find updated schedule: %v", err)
	}
	if updated.LastStatus != "failed" || updated.LastError == "" {
		t.Fatalf("expected schedule failure from sync runner, got %+v", updated)
	}
}

func TestDocumentScheduleService_SourceForDocumentPrefersFeishuWikiSource(t *testing.T) {
	svc := NewDocumentScheduleService(nil, nil, nil, nil, nil, nil, config.RAGKnowledgeScheduleConfig{})
	svc.RegisterSource(&fakeScheduleSource{name: "url"})
	svc.RegisterSource(&fakeScheduleSource{name: "feishu"})

	doc := &knowledgeModel.KnowledgeDocument{
		SourceType:     "url",
		SourceLocation: "https://my.feishu.cn/wiki/RvgUw7bhxi4rUfkwyt6c2LD7nVb",
	}

	got := svc.sourceForDocument(doc)
	if got == nil || got.Name() != "feishu" {
		t.Fatalf("expected feishu source, got %#v", got)
	}
}

func TestDocumentScheduleService_SourceForDocumentPrefersConfluenceSource(t *testing.T) {
	svc := NewDocumentScheduleService(nil, nil, nil, nil, nil, nil, config.RAGKnowledgeScheduleConfig{})
	svc.RegisterSource(&fakeScheduleSource{name: "url"})
	svc.RegisterSource(&fakeScheduleSource{name: "confluence"})

	doc := &knowledgeModel.KnowledgeDocument{
		SourceType:     "url",
		SourceLocation: "https://mycompany.atlassian.net/wiki/spaces/ENG/pages/12345/Confluence+文档",
	}

	got := svc.sourceForDocument(doc)
	if got == nil || got.Name() != "confluence" {
		t.Fatalf("expected confluence source, got %#v", got)
	}
}

func TestDocumentScheduleService_RecoverStuckRunningDocuments(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate documents: %v", err)
	}

	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	oldDoc := &knowledgeModel.KnowledgeDocument{
		KbID:      "kb-1",
		DocName:   "old-running.md",
		FileURL:   "upload://old-running.md",
		FileType:  "md",
		Status:    "running",
		CreatedBy: "user-1",
		UpdatedBy: "user-1",
	}
	oldDoc.ID = "doc-old"
	if err := gdb.Create(oldDoc).Error; err != nil {
		t.Fatalf("seed old doc: %v", err)
	}
	if err := gdb.Exec("UPDATE t_knowledge_document SET update_time = ? WHERE id = ?", now.Add(-11*time.Minute), oldDoc.ID).Error; err != nil {
		t.Fatalf("backdate old doc: %v", err)
	}
	freshDoc := &knowledgeModel.KnowledgeDocument{
		KbID:      "kb-1",
		DocName:   "fresh-running.md",
		FileURL:   "upload://fresh-running.md",
		FileType:  "md",
		Status:    "running",
		CreatedBy: "user-1",
		UpdatedBy: "user-1",
	}
	freshDoc.ID = "doc-fresh"
	if err := gdb.Create(freshDoc).Error; err != nil {
		t.Fatalf("seed fresh doc: %v", err)
	}
	if err := gdb.Exec("UPDATE t_knowledge_document SET update_time = ? WHERE id = ?", now.Add(-5*time.Minute), freshDoc.ID).Error; err != nil {
		t.Fatalf("backdate fresh doc: %v", err)
	}

	svc := NewDocumentScheduleService(gdb, nil, nil, nil, nil, nil, config.RAGKnowledgeScheduleConfig{RunningTimeoutMinutes: 10})
	svc.now = func() time.Time { return now }

	recovered, err := svc.RecoverStuckRunningDocuments(context.Background())
	if err != nil {
		t.Fatalf("recover stuck running documents: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("expected 1 recovered doc, got %d", recovered)
	}

	var oldStored, freshStored knowledgeModel.KnowledgeDocument
	if err := gdb.First(&oldStored, "id = ?", oldDoc.ID).Error; err != nil {
		t.Fatalf("load old doc: %v", err)
	}
	if err := gdb.First(&freshStored, "id = ?", freshDoc.ID).Error; err != nil {
		t.Fatalf("load fresh doc: %v", err)
	}
	if oldStored.Status != "failed" {
		t.Fatalf("expected old doc to be failed, got %s", oldStored.Status)
	}
	if freshStored.Status != "running" {
		t.Fatalf("expected fresh doc to remain running, got %s", freshStored.Status)
	}
}

func TestDocumentScheduleService_RecoverStuckRunningDocumentsUsesConfiguredTimeout(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate documents: %v", err)
	}

	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	oldDoc := &knowledgeModel.KnowledgeDocument{
		KbID:      "kb-1",
		DocName:   "old-running.md",
		FileURL:   "upload://old-running.md",
		FileType:  "md",
		Status:    "running",
		CreatedBy: "user-1",
		UpdatedBy: "user-1",
	}
	oldDoc.ID = "doc-old"
	if err := gdb.Create(oldDoc).Error; err != nil {
		t.Fatalf("seed old doc: %v", err)
	}
	if err := gdb.Exec("UPDATE t_knowledge_document SET update_time = ? WHERE id = ?", now.Add(-21*time.Minute), oldDoc.ID).Error; err != nil {
		t.Fatalf("backdate old doc: %v", err)
	}
	freshDoc := &knowledgeModel.KnowledgeDocument{
		KbID:      "kb-1",
		DocName:   "fresh-running.md",
		FileURL:   "upload://fresh-running.md",
		FileType:  "md",
		Status:    "running",
		CreatedBy: "user-1",
		UpdatedBy: "user-1",
	}
	freshDoc.ID = "doc-fresh"
	if err := gdb.Create(freshDoc).Error; err != nil {
		t.Fatalf("seed fresh doc: %v", err)
	}
	if err := gdb.Exec("UPDATE t_knowledge_document SET update_time = ? WHERE id = ?", now.Add(-19*time.Minute), freshDoc.ID).Error; err != nil {
		t.Fatalf("backdate fresh doc: %v", err)
	}

	svc := NewDocumentScheduleService(gdb, nil, nil, nil, nil, nil, config.RAGKnowledgeScheduleConfig{RunningTimeoutMinutes: 30})
	svc.now = func() time.Time { return now }

	recovered, err := svc.RecoverStuckRunningDocuments(context.Background())
	if err != nil {
		t.Fatalf("recover stuck running documents: %v", err)
	}
	if recovered != 0 {
		t.Fatalf("expected 0 recovered doc with 30-minute timeout, got %d", recovered)
	}

	var oldStored, freshStored knowledgeModel.KnowledgeDocument
	if err := gdb.First(&oldStored, "id = ?", oldDoc.ID).Error; err != nil {
		t.Fatalf("load old doc: %v", err)
	}
	if err := gdb.First(&freshStored, "id = ?", freshDoc.ID).Error; err != nil {
		t.Fatalf("load fresh doc: %v", err)
	}
	if oldStored.Status != "running" {
		t.Fatalf("expected old doc to remain running under 30-minute timeout, got %s", oldStored.Status)
	}
	if freshStored.Status != "running" {
		t.Fatalf("expected fresh doc to remain running, got %s", freshStored.Status)
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}
