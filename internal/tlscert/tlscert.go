// Package tlscert 负责中继用的自签证书：自动生成、加载、计算指纹。
//
// 为什么用自签 + 指纹钉而不是 CA：中心点常常只有公网 IP、没有域名，
// 签不出受信任证书。而 B/C 与 A 之间是「已知对方」的关系——把 A 的证书指纹
// 抄到 B/C 的 relayPin 里，比引入 CA 体系更简单也更难被中间人替换。
package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 证书有效期。自签证书过期会让所有客户端突然连不上，所以给足 10 年。
const validFor = 10 * 365 * 24 * time.Hour

// Ensure 确保证书与私钥可用，返回 PEM 编码的两段内容。
//
// 规则：
//   - certFile/keyFile 都非空：从文件读取（缺失即报错，由调用方决定要不要自动生成）；
//   - 都为空：在 dir 下生成 hub-cert.pem / hub-key.pem（已存在则直接复用）。
//
// hosts 会写进 SAN，供客户端按主机名/IP 校验。
func Ensure(certFile, keyFile, dir string, hosts []string) (certPEM, keyPEM []byte, err error) {
	generated := false
	if certFile == "" && keyFile == "" {
		certFile = filepath.Join(dir, "hub-cert.pem")
		keyFile = filepath.Join(dir, "hub-key.pem")
		generated = true
	}
	certPEM, cErr := os.ReadFile(certFile)
	keyPEM, kErr := os.ReadFile(keyFile)
	if cErr == nil && kErr == nil {
		return certPEM, keyPEM, nil
	}
	if !generated {
		return nil, nil, fmt.Errorf("读取证书失败（cert: %v, key: %v）", cErr, kErr)
	}
	if cErr == nil || kErr == nil {
		// 只剩一半：当成损坏，重新生成（指纹会变，调用方需要提示用户更新 relayPin）
	}

	certPEM, keyPEM, err = Generate(hosts)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		return nil, nil, fmt.Errorf("写入证书失败: %w", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		return nil, nil, fmt.Errorf("写入私钥失败: %w", err)
	}
	return certPEM, keyPEM, nil
}

// Generate 生成一张自签证书（ECDSA P-256），SAN 覆盖 hosts 与回环地址。
func Generate(hosts []string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("生成私钥失败: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("生成序列号失败: %w", err)
	}
	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "clipshare-hub", Organization: []string{"clipshare"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validFor),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	seen := map[string]bool{}
	addHost := func(h string) {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			return
		}
		tmpl.DNSNames = append(tmpl.DNSNames, h)
	}
	addHost("localhost")
	addHost("127.0.0.1")
	addHost("::1")
	for _, h := range hosts {
		addHost(h)
	}
	if hostname, err := os.Hostname(); err == nil {
		addHost(hostname)
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("签发证书失败: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("编码私钥失败: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// parseLeaf 解析 PEM 里的证书（测试与内部校验共用）。
func parseLeaf(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("证书不是合法的 PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

// Fingerprint 返回证书的 "sha256:<64 hex>" 指纹（对 DER 求 SHA-256）。
//
// 与 `openssl x509 -fingerprint -sha256` 的口径一致：算的是证书 DER，不是公钥。
func Fingerprint(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", fmt.Errorf("证书不是合法的 PEM")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return "", fmt.Errorf("解析证书失败: %w", err)
	}
	sum := sha256.Sum256(block.Bytes)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
