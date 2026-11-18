package geminienterprise

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel/vertex"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
)

// 渠道密钥两种写法：
//   - "adc"：在 GCE / Cloud Run / GKE 上用元数据服务器取运行身份的 token，不需要存密钥
//   - 服务账号 JSON：用 JWT 换 token（复用 vertex 渠道的实现）
const keyADC = "adc"

const metadataTokenURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"

type cachedToken struct {
	token     string
	identity  string
	expiresAt time.Time
}

var tokenCache sync.Map // key: 渠道 ID[-多密钥序号]

func isADC(key string) bool { return strings.EqualFold(strings.TrimSpace(key), keyADC) }

// parseServiceAccount 解析服务账号 JSON；密钥是 "adc" 时返回 nil
func parseServiceAccount(key string) (*vertex.Credentials, error) {
	if isADC(key) {
		return nil, nil
	}
	creds := &vertex.Credentials{}
	if err := common.Unmarshal([]byte(key), creds); err != nil {
		return nil, fmt.Errorf("channel key must be \"adc\" or a service account JSON: %w", err)
	}
	if creds.ClientEmail == "" || creds.PrivateKey == "" {
		return nil, fmt.Errorf("service account JSON missing client_email or private_key")
	}
	return creds, nil
}

func getAccessToken(info *relaycommon.RelayInfo) (string, error) {
	cacheKey := fmt.Sprintf("%d", info.ChannelId)
	if info.ChannelIsMultiKey {
		cacheKey = fmt.Sprintf("%d-%d", info.ChannelId, info.ChannelMultiKeyIndex)
	}
	creds, err := parseServiceAccount(info.ApiKey)
	if err != nil {
		return "", err
	}
	identity := keyADC
	if creds != nil {
		identity = creds.ClientEmail + "\x00" + creds.PrivateKeyID // 换了密钥不复用旧 token
	}
	if v, ok := tokenCache.Load(cacheKey); ok {
		e := v.(cachedToken)
		if e.identity == identity && time.Now().Before(e.expiresAt) {
			return e.token, nil
		}
	}

	var token string
	lifetime := 50 * time.Minute // 服务账号 token 有效期 60 分钟，提前 10 分钟刷新
	if creds == nil {
		token, lifetime, err = metadataToken()
	} else {
		token, err = vertex.AcquireAccessToken(*creds, info.ChannelSetting.Proxy)
	}
	if err != nil {
		return "", err
	}
	tokenCache.Store(cacheKey, cachedToken{token: token, identity: identity, expiresAt: time.Now().Add(lifetime)})
	return token, nil
}

func metadataToken() (string, time.Duration, error) {
	req, _ := http.NewRequest(http.MethodGet, metadataTokenURL, nil)
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := service.GetHttpClient().Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("metadata server unreachable (key \"adc\" only works on GCE/Cloud Run/GKE): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("metadata token HTTP %d", resp.StatusCode)
	}
	var r struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", 0, err
	}
	life := time.Duration(r.ExpiresIn)*time.Second - 5*time.Minute
	if life <= 0 {
		life = time.Minute
	}
	return r.AccessToken, life, nil
}
