package importer

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Credential struct {
	Identity    string
	Email       string
	Provider    string
	AccountID   string
	WorkspaceID string
	Auth        map[string]any
}

func Parse(raw json.RawMessage) ([]Credential, error) {
	if len(raw) == 0 {
		return nil, errors.New("credential is required")
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("credential must be valid JSON: %w", err)
	}
	var result []Credential
	seen := make(map[string]struct{})
	visit(document, &result, seen)
	if len(result) == 0 {
		return nil, errors.New("no supported credential was found")
	}
	return result, nil
}

func visit(value any, result *[]Credential, seen map[string]struct{}) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			visit(item, result, seen)
		}
	case map[string]any:
		if converted, ok := convert(typed); ok {
			if _, exists := seen[converted.Identity]; !exists {
				seen[converted.Identity] = struct{}{}
				*result = append(*result, converted)
			}
			return
		}
		for _, nested := range typed {
			visit(nested, result, seen)
		}
	}
}

func convert(candidate map[string]any) (Credential, bool) {
	accessToken := firstString(candidate, "access_token", "accessToken")
	credentialMap := firstMap(candidate, "credentials", "credential", "tokens", "token", "auth")
	if accessToken == "" && credentialMap != nil {
		accessToken = firstString(credentialMap, "access_token", "accessToken")
	}
	if accessToken == "" {
		return Credential{}, false
	}

	claims := jwtClaims(accessToken)
	email := firstNonEmpty(
		firstString(candidate, "email", "account_email", "mail"),
		firstString(credentialMap, "email", "account_email", "mail"),
		firstString(claims, "email"),
	)
	accountID := firstNonEmpty(
		firstString(candidate, "account_id", "accountId"),
		firstString(credentialMap, "account_id", "accountId"),
		claimString(claims, "chatgpt_account_id", "account_id", "accountId"),
	)
	chatgptAccountID := firstNonEmpty(
		firstString(candidate, "chatgpt_account_id", "chatgptAccountId"),
		firstString(credentialMap, "chatgpt_account_id", "chatgptAccountId"),
		claimString(claims, "chatgpt_account_id"),
	)
	workspaceID := firstNonEmpty(
		firstString(candidate, "workspace_id", "workspaceId", "organization_id", "organizationId", "team_id", "teamId"),
		firstString(firstMap(candidate, "meta", "metadata", "providerSpecificData"), "workspace_id", "workspaceId", "organization_id", "organizationId", "team_id", "teamId"),
		firstString(credentialMap, "workspace_id", "workspaceId", "organization_id", "organizationId", "team_id", "teamId"),
		claimString(claims, "workspace_id", "workspaceId", "organization_id", "organizationId", "team_id", "teamId"),
	)
	provider := normalizeProvider(firstNonEmpty(
		firstString(candidate, "platform", "provider"),
		firstString(credentialMap, "platform", "provider"),
		firstString(candidate, "type"),
	))

	auth := map[string]any{
		"type":         provider,
		"access_token": accessToken,
	}
	copyString(auth, "refresh_token", candidate, credentialMap, "refresh_token", "refreshToken")
	copyString(auth, "id_token", candidate, credentialMap, "id_token", "idToken")
	copyString(auth, "session_token", candidate, credentialMap, "session_token", "sessionToken")
	copyString(auth, "plan_type", candidate, credentialMap, "plan_type", "planType")
	copyString(auth, "chatgpt_plan_type", candidate, credentialMap, "chatgpt_plan_type", "chatgptPlanType")
	copyValue(auth, "expired", candidate, credentialMap, "expired", "expires_at")
	copyValue(auth, "disabled", candidate, credentialMap, "disabled")
	if email != "" {
		auth["email"] = email
		auth["name"] = email
	}
	if accountID != "" {
		auth["account_id"] = accountID
	}
	if chatgptAccountID != "" {
		auth["chatgpt_account_id"] = chatgptAccountID
	}
	if workspaceID != "" {
		auth["workspace_id"] = workspaceID
	}

	identityMaterial := strings.Join([]string{provider, strings.ToLower(email), accountID, chatgptAccountID, workspaceID}, "\x00")
	if accountID == "" && chatgptAccountID == "" && workspaceID == "" {
		identityMaterial += "\x00" + secretFingerprint(auth)
	}
	sum := sha256.Sum256([]byte(identityMaterial))
	return Credential{
		Identity:    hex.EncodeToString(sum[:]),
		Email:       email,
		Provider:    provider,
		AccountID:   accountID,
		WorkspaceID: workspaceID,
		Auth:        auth,
	}, true
}

func normalizeProvider(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "oauth", "openai", "chatgpt", "codex":
		return "codex"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func firstMap(source map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if source == nil {
			continue
		}
		if value, ok := source[key].(map[string]any); ok {
			return value
		}
	}
	return nil
}

func firstString(source map[string]any, keys ...string) string {
	for _, key := range keys {
		if source == nil {
			continue
		}
		if value, ok := source[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func copyString(dst map[string]any, key string, sources ...any) {
	var aliases []string
	var maps []map[string]any
	for _, value := range sources {
		switch typed := value.(type) {
		case map[string]any:
			maps = append(maps, typed)
		case string:
			aliases = append(aliases, typed)
		}
	}
	for _, source := range maps {
		if value := firstString(source, aliases...); value != "" {
			dst[key] = value
			return
		}
	}
}

func copyValue(dst map[string]any, key string, first, second map[string]any, aliases ...string) {
	for _, source := range []map[string]any{first, second} {
		for _, alias := range aliases {
			if source == nil {
				continue
			}
			if value, ok := source[alias]; ok && value != nil {
				dst[key] = value
				return
			}
		}
	}
}

func secretFingerprint(auth map[string]any) string {
	material := firstString(auth, "access_token", "refresh_token", "session_token")
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}

func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

func claimString(claims map[string]any, keys ...string) string {
	if value := firstString(claims, keys...); value != "" {
		return value
	}
	for _, namespace := range []string{
		"https://api.openai.com/auth",
		"https://api.openai.com/profile",
		"https://api.openai.com/claims",
	} {
		if value := firstString(firstMap(claims, namespace), keys...); value != "" {
			return value
		}
	}
	return ""
}
