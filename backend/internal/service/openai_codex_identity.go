package service

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
)

// codexUpstreamMinVersion 上游 /backend-api/codex 接受的最低 version 头：
// 若请求携带 version 且低于该值，上游直接 404（issue #3901，2026-07 实测）。
const codexUpstreamMinVersion = "0.144.0"

// codexClientVersionMaxLen 官方版本号均为短 ASCII 串，远低于此上限。
const codexClientVersionMaxLen = 64

// codexClientVersionPattern 允许 0.146.0 与 0.147.0-alpha.4 两类官方形态。
var codexClientVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}(-[0-9A-Za-z.]+)?$`)

// NormalizeCodexClientVersion 校验并归一化 Codex 客户端版本号，非法值返回空串。
// 该值会被拼进出站 User-Agent 与 version 头，必须拒绝任意字节，避免管理员误填或
// 自动同步拿到异常值时把不可控内容透给上游。
func NormalizeCodexClientVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || len(version) > codexClientVersionMaxLen || !codexClientVersionPattern.MatchString(version) {
		return ""
	}
	return version
}

// buildCodexCLIUserAgent 按版本号拼出规范 Codex TUI User-Agent。
// UA 形态只在 codexCLIUserAgentSuffix 一处定义，避免多处拼装漂移。
func buildCodexCLIUserAgent(version string) string {
	if version = NormalizeCodexClientVersion(version); version == "" || CompareVersions(version, codexUpstreamMinVersion) < 0 {
		return codexCLIUserAgent
	}
	return openai.CodexDefaultOriginator + "/" + version + codexCLIUserAgentSuffix
}

// codexIdentityEnforcement 控制是否强制统一出站身份，
// 由 gateway.disable_codex_identity_enforcement 在服务构造时取反发布。
// 默认开启，保持出站 User-Agent / originator / version 同源一致。
// 统一身份不保证避免上游容量错误；关闭后按客户端 UA 对齐身份和版本。
var codexIdentityEnforcement = func() *atomic.Bool {
	v := &atomic.Bool{}
	v.Store(true)
	return v
}()

// SetCodexIdentityEnforcementEnabled 发布 Codex 出站身份强制统一开关。
// 由持有配置的服务发布；每次身份解析读取一次，投影阶段不重新读取。
func SetCodexIdentityEnforcementEnabled(enabled bool) {
	codexIdentityEnforcement.Store(enabled)
}

// codexCanonicalUserAgentResolver 返回当前生效的规范 Codex User-Agent（后台设置 / 自动同步版本号）。
// 由 SettingService 在装配时注入；解析器内部自带 TTL 缓存，热路径不触库。
type codexCanonicalUserAgentResolver func() string

var (
	codexCanonicalUAMu       sync.RWMutex
	codexCanonicalUAResolver codexCanonicalUserAgentResolver
)

// SetCodexCanonicalUserAgentResolver 注入规范 User-Agent 解析器。
// 未注入或解析结果非法时回退到编译期常量 codexCLIUserAgent。
func SetCodexCanonicalUserAgentResolver(resolver func() string) {
	codexCanonicalUAMu.Lock()
	defer codexCanonicalUAMu.Unlock()
	codexCanonicalUAResolver = resolver
}

// CodexCanonicalUserAgent 返回当前生效的规范 Codex User-Agent。
// 取值走与推理相同的解析链：完整自定义 UA，或按版本设置生成的默认 CLI UA。
// 供无账号句柄的出站路径（OAuth 换 Token / 刷新）使用。
func CodexCanonicalUserAgent() string {
	return resolveCodexOutboundIdentity("").userAgent
}

// CodexCanonicalAuthIdentity 返回凭据面（auth.openai.com：换 Token / 刷新 / whoami）
// 出站请求的身份对：规范 User-Agent 与配套 originator，与推理解析链同源。
// 凭据面不发 version 头——真实 Codex 客户端在该面只携带 originator 与 User-Agent
// （codex-rs login/default_client.rs 的 default_headers()），version 门槛
// （issue #3901）只存在于 /backend-api/codex 推理面。
func CodexCanonicalAuthIdentity() (userAgent, originator string) {
	identity := resolveCodexOutboundIdentity("")
	return identity.userAgent, identity.originator
}

// ApplyCodexCanonicalAuthIdentity 为凭据面出站请求写入身份对（不含 version）。
func ApplyCodexCanonicalAuthIdentity(h http.Header) {
	if h == nil {
		return
	}
	userAgent, originator := CodexCanonicalAuthIdentity()
	h.Set("user-agent", userAgent)
	h.Set("originator", originator)
}

// CodexCanonicalClientVersion 返回当前生效的 Codex 客户端版本号。
func CodexCanonicalClientVersion() string {
	return resolveCodexOutboundIdentity("").version
}

// codexCanonicalUserAgent 返回出站规范 User-Agent。
func codexCanonicalUserAgent() string {
	codexCanonicalUAMu.RLock()
	resolver := codexCanonicalUAResolver
	codexCanonicalUAMu.RUnlock()
	if resolver != nil {
		if ua := resolver(); strings.TrimSpace(ua) != "" {
			return ua
		}
	}
	return codexCLIUserAgent
}

// codexOutboundIdentity 出站身份三元组，三者必须同源自洽：
// originator 与 User-Agent 首段配套（否则上游 404，issue #3901），
// version 等于 User-Agent 的版本段且不低于上游门槛。
type codexOutboundIdentity struct {
	userAgent  string
	originator string
	version    string
}

func (i codexOutboundIdentity) applyHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	headers.Set("User-Agent", i.userAgent)
	headers.Set("originator", i.originator)
	headers.Set("version", i.version)
}

func resolveCodexRequestClientIdentity(inputUA, configuredUA string, forceCLI bool) codexOutboundIdentity {
	canonical := codexCanonicalUserAgent()
	if forceCLI {
		configuredUA = ""
		inputUA = canonical
	}
	if !codexIdentityEnforcement.Load() && configuredUA == "" {
		configuredUA = inputUA
	}
	return resolveCodexOutboundIdentityWithCanonical(configuredUA, canonical)
}

func (s *OpenAIGatewayService) resolveCodexAccountClientIdentity(account, source *Account, inputUA string) codexOutboundIdentity {
	forceCLI := s != nil && s.cfg != nil && s.cfg.Gateway.ForceCodexCLI
	override := source.GetOpenAIUserAgent()
	if source != account && account.GetOpenAIUserAgent() != "" {
		override = account.GetOpenAIUserAgent()
	}
	return resolveCodexRequestClientIdentity(inputUA, override, forceCLI)
}

// resolveCodexOutboundIdentity 由候选 User-Agent 推导自洽的出站身份。
// candidateUA 为空时使用规范 User-Agent；推导不出官方身份时整体回退为规范 TUI 身份。
//
// 管理员填写的完整 UA 保留全部版本。只有默认 CLI UA 的生成使用版本设置。
func resolveCodexOutboundIdentity(candidateUA string) codexOutboundIdentity {
	return resolveCodexOutboundIdentityWithCanonical(candidateUA, codexCanonicalUserAgent())
}

func resolveCodexOutboundIdentityWithCanonical(candidateUA, canonical string) codexOutboundIdentity {
	for _, ua := range []string{candidateUA, canonical} {
		if identity, ok := codexOutboundIdentityFromUA(ua); ok {
			return identity
		}
	}
	return codexOutboundIdentity{userAgent: codexCLIUserAgent, originator: openai.CodexDefaultOriginator, version: codexCLIVersion}
}

func codexOutboundIdentityFromUA(ua string) (codexOutboundIdentity, bool) {
	profile, ok := openai.ParseCodexWireProfile(ua)
	if !ok || NormalizeCodexClientVersion(profile.CoreVersion) != profile.CoreVersion || CompareVersions(profile.CoreVersion, codexUpstreamMinVersion) < 0 {
		return codexOutboundIdentity{}, false
	}
	if profile.ClientInfo != nil && NormalizeCodexClientVersion(profile.ClientInfo.Version) == "" {
		return codexOutboundIdentity{}, false
	}
	return codexOutboundIdentity{userAgent: profile.UserAgent, originator: profile.Originator, version: profile.CoreVersion}, true
}

// ValidateOpenAICodexUserAgent shares the outbound parser with settings writes.
func ValidateOpenAICodexUserAgent(ua string) error {
	if strings.Trim(ua, " ") == "" {
		return nil
	}
	if len(ua) > 512 {
		return fmt.Errorf("openai_codex_user_agent must be at most 512 characters")
	}
	if _, ok := codexOutboundIdentityFromUA(ua); !ok {
		return fmt.Errorf("openai_codex_user_agent must be a valid Codex UA with Core version >= %s", codexUpstreamMinVersion)
	}
	return nil
}

// ensureCodexIdentityHeaders 补齐 OAuth（ChatGPT 内部接口）出站请求所需的 Codex 身份头。
// 用于没有真实用户身份的合成探测；已有请求头保持不变。
func ensureCodexIdentityHeaders(h http.Header) {
	if h == nil {
		return
	}
	identity := resolveCodexOutboundIdentity("")
	if strings.TrimSpace(h.Get("user-agent")) == "" {
		h.Set("user-agent", identity.userAgent)
	}
	if strings.TrimSpace(h.Get("originator")) == "" {
		h.Set("originator", identity.originator)
	}
	if strings.TrimSpace(h.Get("version")) == "" {
		h.Set("version", identity.version)
	}
	h.Set("OpenAI-Beta", "responses=experimental")
}

// applyOpenAICodexProbeHeaders 为合成探测请求补齐 Codex 身份和引擎指纹。
func applyOpenAICodexProbeHeaders(h http.Header) {
	if h == nil {
		return
	}
	ensureCodexIdentityHeaders(h)
	h.Set("X-Codex-Window-ID", uuid.NewString())
}

func pairCodexIdentityHeadersWithCanonical(h http.Header, canonical string) {
	resolveCodexOutboundIdentityWithCanonical(h.Get("user-agent"), canonical).applyHeaders(h)
}
