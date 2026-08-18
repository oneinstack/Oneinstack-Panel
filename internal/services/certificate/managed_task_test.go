package certificate

import (
	"testing"

	"oneinstack/internal/models"
)

func TestManagedSelfSignedTaskCreatesResource(t *testing.T) {
	db := openCertificateTestDB(t)
	manager := newCertificateTestManager(t, db, &fakeIssuer{}, &fakeDeployer{})
	defer stopCertificateTestManager(t, manager)

	task, err := manager.SubmitManagedSelfSigned(SelfSignedOptions{
		Domains: []string{"task.example.com"}, Algorithm: "ec-256", ValidityYears: 1,
	}, 7)
	if err != nil {
		t.Fatal(err)
	}
	completed := waitCertificateTask(t, manager, task.ID)
	if completed.Status != models.CertificateTaskStatusSucceeded || completed.Operation != models.CertificateTaskOperationSelfSigned || completed.ManagedID == "" {
		t.Fatalf("unexpected managed task: %#v", completed)
	}
	var record models.ManagedCertificate
	if err := db.First(&record, "id = ?", completed.ManagedID).Error; err != nil {
		t.Fatal(err)
	}
	if record.Algorithm != "ec-256" {
		t.Fatalf("algorithm = %s", record.Algorithm)
	}
}

func TestManagedUploadTaskClearsTemporaryMaterial(t *testing.T) {
	db := openCertificateTestDB(t)
	deployer := &fakeDeployer{}
	manager := newCertificateTestManager(t, db, &fakeIssuer{}, deployer)
	defer stopCertificateTestManager(t, manager)

	catalog := NewCatalog(db, manager.certificateRoot, deployer)
	source, err := catalog.CreateSelfSigned(SelfSignedOptions{Domains: []string{"upload.example.com"}, Algorithm: "ec-256", ValidityYears: 1})
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM, _, err := catalog.ReadMaterial(source.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPEM, _, err := catalog.ReadMaterial(source.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	task, err := manager.SubmitManagedUpload(CreateCertificateOptions{CertificatePEM: certificatePEM, PrivateKeyPEM: privateKeyPEM}, 10)
	if err != nil {
		t.Fatal(err)
	}
	completed := waitCertificateTask(t, manager, task.ID)
	if completed.Status != models.CertificateTaskStatusSucceeded || completed.ManagedID == "" {
		t.Fatalf("upload task = %#v", completed)
	}
	if completed.InputCertPath != "" || completed.InputKeyPath != "" {
		t.Fatalf("temporary paths leaked into task: %#v", completed)
	}
}

func TestManagedBindTaskDeploysAndCreatesLegacyProjection(t *testing.T) {
	db := openCertificateTestDB(t)
	website := createCertificateTestWebsite(t, db)
	deployer := &fakeDeployer{}
	manager := newCertificateTestManager(t, db, &fakeIssuer{}, deployer)
	defer stopCertificateTestManager(t, manager)

	catalog := NewCatalog(db, manager.certificateRoot, deployer)
	record, err := catalog.CreateSelfSigned(SelfSignedOptions{Domains: []string{"example.com", "www.example.com"}, Algorithm: "ec-256", ValidityYears: 1})
	if err != nil {
		t.Fatal(err)
	}
	task, err := manager.SubmitManagedBind(record.ID, website.ID, true, 8)
	if err != nil {
		t.Fatal(err)
	}
	completed := waitCertificateTask(t, manager, task.ID)
	if completed.Status != models.CertificateTaskStatusSucceeded {
		t.Fatalf("bind task = %#v", completed)
	}
	var binding models.CertificateBinding
	if err := db.Where("managed_certificate_id = ? AND website_id = ?", record.ID, website.ID).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	if binding.Status != BindingStatusActive || !binding.ForceHTTPS {
		t.Fatalf("binding = %#v", binding)
	}
	var legacy models.Certificate
	if err := db.Where("website_id = ?", website.ID).First(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if legacy.ManagedID != record.ID || !legacy.ForceHTTPS {
		t.Fatalf("legacy projection = %#v", legacy)
	}
	if deployer.deployCalls != 1 {
		t.Fatalf("deploy calls = %d", deployer.deployCalls)
	}
}

func TestManagedTaskCancellationStillUsesCommonTaskState(t *testing.T) {
	db := openCertificateTestDB(t)
	manager := newCertificateTestManager(t, db, &fakeIssuer{}, &fakeDeployer{})
	defer stopCertificateTestManager(t, manager)

	task, err := manager.SubmitManagedSelfSigned(SelfSignedOptions{Domains: []string{"cancel.example.com"}, Algorithm: "rsa-2048"}, 9)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	completed := waitCertificateTask(t, manager, task.ID)
	if !models.IsCertificateTaskTerminal(completed.Status) {
		t.Fatalf("task status = %s", completed.Status)
	}
}
