package tlscert

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateAndFingerprintStable(t *testing.T) {
	certPEM, keyPEM, err := Generate([]string{"101.201.30.29", "hub.example.com"})
	if err != nil {
		t.Fatalf("生成证书失败: %v", err)
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Fatal("PEM 为空")
	}
	fp1, err := Fingerprint(certPEM)
	if err != nil {
		t.Fatalf("计算指纹失败: %v", err)
	}
	fp2, _ := Fingerprint(certPEM)
	if fp1 != fp2 {
		t.Fatal("同一张证书的指纹应稳定")
	}
	if !strings.HasPrefix(fp1, "sha256:") || len(fp1) != len("sha256:")+64 {
		t.Fatalf("指纹格式不对: %q", fp1)
	}
}

func TestGenerateIncludesSANs(t *testing.T) {
	certPEM, _, err := Generate([]string{"101.201.30.29", "hub.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	// 用 tlscert 自己的解析路径验证 SAN：证书里应能看到这个 IP 与域名
	cert, err := parseLeaf(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	var hasIP, hasDNS bool
	for _, ip := range cert.IPAddresses {
		if ip.String() == "101.201.30.29" {
			hasIP = true
		}
	}
	for _, d := range cert.DNSNames {
		if d == "hub.example.com" {
			hasDNS = true
		}
	}
	if !hasIP || !hasDNS {
		t.Fatalf("SAN 不完整：IP=%v DNS=%v", cert.IPAddresses, cert.DNSNames)
	}
}

func TestEnsureReusesExistingFiles(t *testing.T) {
	dir := t.TempDir()
	certPEM, _, err := Ensure("", "", dir, nil)
	if err != nil {
		t.Fatalf("首次生成失败: %v", err)
	}
	fp1, _ := Fingerprint(certPEM)

	// 第二次必须复用同一份文件（否则每次重启指纹都变，客户端全部连不上）
	certPEM2, _, err := Ensure("", "", dir, nil)
	if err != nil {
		t.Fatalf("二次读取失败: %v", err)
	}
	fp2, _ := Fingerprint(certPEM2)
	if fp1 != fp2 {
		t.Fatalf("指纹不应变化: %s → %s", fp1, fp2)
	}
	for _, name := range []string{"hub-cert.pem", "hub-key.pem"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("缺少文件 %s", name)
		}
	}
}

func TestEnsureExplicitMissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	_, _, err := Ensure(filepath.Join(dir, "nope.pem"), filepath.Join(dir, "nope-key.pem"), dir, nil)
	if err == nil {
		t.Fatal("显式指定的证书文件不存在时应报错，而不是静默生成")
	}
}
