package i18n

func init() {
	translations := map[string]string{
		"Panel 地址检测参数无效": "The Panel URL check parameters are invalid.",
		"Panel 地址格式无效，请填写不含账号、查询参数或锚点的 HTTP/HTTPS 地址。":          "The Panel URL is invalid. Enter an HTTP/HTTPS URL without credentials, query parameters, or fragments.",
		"为避免访问本机、链路本地或保留服务地址，主控未对该地址发起检测。":                      "The controller did not probe the URL because it resolves to a local, link-local, or reserved service address.",
		"目标主机没有可用的 IP 地址，请检查域名和 DNS 配置。":                        "The target host has no usable IP address. Check the domain and DNS configuration.",
		"目标地址解析失败，请检查域名和主控服务器的 DNS 配置。":                         "The target could not be resolved. Check the domain and the controller DNS configuration.",
		"目标地址检测超时，请检查地址、端口、防火墙和网络访问策略。":                         "The target URL check timed out. Check the URL, port, firewall, and network access policy.",
		"目标地址拒绝连接，请确认 Panel 服务已启动且端口正确。":                        "The target refused the connection. Confirm that the Panel service is running on the configured port.",
		"目标 HTTPS 证书校验失败，请检查证书有效期、域名和证书链。":                      "The target HTTPS certificate could not be verified. Check its validity period, hostname, and certificate chain.",
		"无法访问目标地址，请检查网络、端口、代理和防火墙配置。":                           "The target could not be reached. Check the network, port, proxy, and firewall configuration.",
		"Panel 健康检查地址发生跳转，请填写跳转后的最终面板地址。":                       "The Panel health URL redirected. Enter the final Panel URL.",
		"目标地址可以访问，但未返回 OneinStack Panel 健康检查响应。":                "The target is reachable but did not return a OneinStack Panel health response.",
		"目标地址可以访问，但无法确认它是 OneinStack Panel。":                    "The target is reachable, but it could not be identified as OneinStack Panel.",
		"已识别 OneinStack Panel，但数据库或前端资源尚未就绪。":                   "OneinStack Panel was detected, but its database or web UI is not ready.",
		"面板地址发生跳转，请填写跳转后的最终地址。":                                 "The Panel URL redirected. Enter the final URL.",
		"目标 Panel 已就绪，但填写的面板入口返回了异常 HTTP 状态。":                   "The target Panel is ready, but the configured entry URL returned an unexpected HTTP status.",
		"目标 Panel 已就绪，但填写的地址不是有效的 Panel 页面；如已启用安全入口，请补充正确入口路径。": "The target Panel is ready, but the configured URL is not a valid Panel page. Include the correct secure entry path if it is enabled.",
		"地址检测通过，目标 OneinStack Panel 已就绪。":                       "The URL check passed and the target OneinStack Panel is ready.",

		// Endpoint address mismatch notes
		"controllerUrl 中的 /v1 后缀已自动移除。Agent 端点无需 /v1 前缀。":                        "The /v1 suffix in controllerUrl was automatically removed. Agent endpoints do not require the /v1 prefix.",
		"节点报告私有 IP 而端点使用公网 IP，但心跳正常，NAT 配置有效。":                                   "The node reports a private IP while the endpoint uses a public IP, but heartbeats are working. NAT is configured correctly.",
		"节点报告私有 IP 而端点使用公网 IP，且节点离线。请检查 NAT、安全组和防火墙配置。":                          "The node reports a private IP while the endpoint uses a public IP, and the node is offline. Check NAT, security group, and firewall settings.",
		"端点配置为私有 IP 而节点报告公网 IP，但心跳正常。":                                           "The endpoint is configured with a private IP while the node reports a public IP, but heartbeats are working.",
		"端点配置为私有 IP 而节点报告公网 IP，且节点离线。请确认网络可达性。":                                  "The endpoint is configured with a private IP while the node reports a public IP, and the node is offline. Verify network reachability.",
		"端点 IP 与节点报告 IP 不同，但心跳正常。可能有多网卡或代理配置。":                                   "The endpoint IP differs from the node-reported IP, but heartbeats are working. This may be due to multiple NICs or proxy configuration.",
		"端点 IP 与节点报告 IP 不同，且节点离线。请检查端点地址配置。":                                    "The endpoint IP differs from the node-reported IP, and the node is offline. Check the endpoint address configuration.",
	}
	for source, translated := range translations {
		englishErrorTexts[source] = translated
	}
}
