package filemanager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	DefaultRemoteDownloadLimit int64 = 100 << 20
	defaultRequestTimeout            = 5 * time.Minute
	defaultDialTimeout               = 10 * time.Second
)

var (
	ErrUnsafeRemoteURL = errors.New("unsafe remote URL")
	ErrDownloadLimit   = errors.New("download size limit exceeded")
)

var blockedAddressPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

type Downloader struct {
	client   *http.Client
	maxBytes int64
}

func NewDownloader() *Downloader {
	dialer := &net.Dialer{
		Timeout:   defaultDialTimeout,
		KeepAlive: 30 * time.Second,
	}
	resolver := net.DefaultResolver

	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("解析远程地址失败: %w", err)
			}
			addresses, err := resolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("解析远程主机失败: %w", err)
			}
			if len(addresses) == 0 {
				return nil, errors.New("远程主机没有可用地址")
			}
			for _, address := range addresses {
				if !isPublicAddress(address) {
					return nil, fmt.Errorf("%w: 禁止访问非公网地址 %s", ErrUnsafeRemoteURL, address)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}

	downloader := &Downloader{
		client: &http.Client{
			Transport: transport,
			Timeout:   defaultRequestTimeout,
		},
		maxBytes: DefaultRemoteDownloadLimit,
	}
	downloader.client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("%w: 远程下载重定向次数过多", ErrUnsafeRemoteURL)
		}
		return ValidateRemoteURL(request.URL)
	}
	return downloader
}

func ValidateRemoteURL(remoteURL *url.URL) error {
	if remoteURL == nil {
		return fmt.Errorf("%w: 下载地址为空", ErrUnsafeRemoteURL)
	}
	if remoteURL.Scheme != "http" && remoteURL.Scheme != "https" {
		return fmt.Errorf("%w: 仅支持 http 或 https 下载地址", ErrUnsafeRemoteURL)
	}
	if remoteURL.User != nil {
		return fmt.Errorf("%w: 下载地址不能包含用户凭据", ErrUnsafeRemoteURL)
	}

	host := strings.TrimSuffix(strings.ToLower(remoteURL.Hostname()), ".")
	if host == "" {
		return fmt.Errorf("%w: 下载地址缺少主机名", ErrUnsafeRemoteURL)
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("%w: 禁止访问本地主机", ErrUnsafeRemoteURL)
	}
	if address, err := netip.ParseAddr(host); err == nil && !isPublicAddress(address) {
		return fmt.Errorf("%w: 禁止访问非公网地址 %s", ErrUnsafeRemoteURL, address)
	}
	return nil
}

func ParseAndValidateRemoteURL(rawURL string) (*url.URL, error) {
	remoteURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("%w: 下载地址格式错误: %v", ErrUnsafeRemoteURL, err)
	}
	if err := ValidateRemoteURL(remoteURL); err != nil {
		return nil, err
	}
	return remoteURL, nil
}

func isPublicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() ||
		!address.IsGlobalUnicast() ||
		address.IsPrivate() ||
		address.IsLoopback() ||
		address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() ||
		address.IsMulticast() ||
		address.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedAddressPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func (d *Downloader) Download(ctx context.Context, manager *Manager, rawURL, virtualDir, name string, limitOverride ...int64) (err error) {
	if d == nil || d.client == nil || d.maxBytes <= 0 {
		return errors.New("远程下载服务未正确初始化")
	}
	maxBytes := d.maxBytes
	if len(limitOverride) > 0 {
		if limitOverride[0] <= 0 {
			return ErrDownloadLimit
		}
		maxBytes = limitOverride[0]
	}
	if err := ValidateName(name); err != nil {
		return err
	}
	target, err := manager.Join(virtualDir, name)
	if err != nil {
		return err
	}
	remoteURL, err := ParseAndValidateRemoteURL(rawURL)
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteURL.String(), nil)
	if err != nil {
		return fmt.Errorf("创建下载请求失败: %w", err)
	}
	response, err := d.client.Do(request)
	if err != nil {
		return fmt.Errorf("远程下载失败: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("远程服务器返回异常状态: %s", response.Status)
	}
	if response.ContentLength > maxBytes {
		return fmt.Errorf("%w: 远程文件超过 %d 字节限制", ErrDownloadLimit, maxBytes)
	}

	file, err := manager.OpenFileRelative(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	keepFile := false
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		if err != nil || !keepFile {
			_ = manager.RemoveAll(manager.VirtualPath(target))
		}
	}()

	written, err := io.Copy(file, io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return fmt.Errorf("写入下载文件失败: %w", err)
	}
	if written > maxBytes {
		return fmt.Errorf("%w: 远程文件超过 %d 字节限制", ErrDownloadLimit, maxBytes)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("同步下载文件失败: %w", err)
	}
	keepFile = true
	return nil
}
