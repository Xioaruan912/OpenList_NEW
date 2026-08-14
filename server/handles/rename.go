package handles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// ==================== 规则引擎 ====================

var (
	prefixBracketRe = regexp.MustCompile(`^\[[^\]]{1,20}\]`) // [MD5] [new] 等前缀
	md5PrefixRe     = regexp.MustCompile(`^MD5[\s#_\-]*`)
	advertRe        = regexp.MustCompile(`【[^】]{0,20}】|更多\s*TG\s*搜@[^\s_#]+|TG\s*@[^\s_#]+|t\.me/[^\s_#]+`)
	longNumberRe    = regexp.MustCompile(`\d{8,}`)                                 // 长数字 ID
	imgVideoRe      = regexp.MustCompile(`(?:^|[_\-])(IMG|VID|video)[_\-]?\d{3,}`) // 相机默认名
	dateRe          = regexp.MustCompile(`(19|20)\d{2}[-_/]?\d{1,2}[-_/]?\d{1,2}`)
	tagRe           = regexp.MustCompile(`#([^#_;\s]+)`)
	pureNumberRe    = regexp.MustCompile(`^\d+$`)
	unclassifiedRe  = regexp.MustCompile(`[\s_\-\(\)\[\]]+`)
)

// RenameSuggestion 重命名建议
type RenameSuggestion struct {
	OldName string `json:"old_name"`
	NewName string `json:"new_name"`
	Reason  string `json:"reason"`
}

// ruleRenameName 规则引擎：提取标签、去广告/前缀、日期识别，生成新名字
func ruleRenameName(oldName string) RenameSuggestion {
	ext := utils.Ext(oldName)
	base := strings.TrimSuffix(oldName, ext)
	reasonParts := []string{}

	// 1. 提取 #标签
	var tags []string
	seen := map[string]bool{}
	for _, m := range tagRe.FindAllStringSubmatch(base, -1) {
		t := strings.TrimSpace(m[1])
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		tags = append(tags, t)
	}
	if len(tags) > 0 {
		reasonParts = append(reasonParts, "提取标签")
	}

	// 2. 去除广告/前缀/数字 ID/相机默认名
	cleaned := base
	cleaned = prefixBracketRe.ReplaceAllString(cleaned, "")
	cleaned = md5PrefixRe.ReplaceAllString(cleaned, "")
	cleaned = advertRe.ReplaceAllString(cleaned, "")
	cleaned = longNumberRe.ReplaceAllString(cleaned, "")
	cleaned = imgVideoRe.ReplaceAllString(cleaned, "")
	if cleaned != base {
		reasonParts = append(reasonParts, "清理杂乱信息")
	}

	// 3. 日期
	var dateStr string
	if m := dateRe.FindString(base); m != "" {
		d := strings.ReplaceAll(m, "_", "-")
		d = strings.ReplaceAll(d, "/", "-")
		parts := strings.Split(d, "-")
		if len(parts) == 3 {
			if len(parts[1]) == 1 {
				parts[1] = "0" + parts[1]
			}
			if len(parts[2]) == 1 {
				parts[2] = "0" + parts[2]
			}
			dateStr = parts[0] + "-" + parts[1] + "-" + parts[2]
		} else {
			dateStr = d
		}
		reasonParts = append(reasonParts, "识别日期")
	}

	// 4. 组装
	var nameParts []string
	if dateStr != "" {
		nameParts = append(nameParts, dateStr)
	}
	nameParts = append(nameParts, tags...)

	// 剩余可读内容（去标签/日期/数字后的残留）
	remain := unclassifiedRe.ReplaceAllString(cleaned, "")
	for _, t := range tags {
		remain = strings.ReplaceAll(remain, t, "")
	}
	if dateStr != "" {
		remain = strings.ReplaceAll(remain, strings.ReplaceAll(dateStr, "-", ""), "")
		remain = strings.ReplaceAll(remain, dateStr, "")
	}
	remain = unclassifiedRe.ReplaceAllString(remain, "")

	// 残留以数字为主（如 new6625626）视为无意义，归未分类
	if remain != "" {
		letters := regexp.MustCompile(`[a-zA-Z]`).FindAllString(remain, -1)
		digits := regexp.MustCompile(`\d`).FindAllString(remain, -1)
		if len(digits) >= len(letters) && len(digits) > 0 {
			remain = ""
		}
	}
	if len(nameParts) == 0 {
		if remain != "" && !pureNumberRe.MatchString(remain) {
			if len(remain) > 30 {
				remain = remain[:30]
			}
			nameParts = append(nameParts, remain)
			reasonParts = append(reasonParts, "保留可读片段")
		} else {
			nameParts = append(nameParts, "未分类")
			reasonParts = append(reasonParts, "无法识别")
		}
	}

	extLower := strings.ToLower(ext)
	if extLower != "" && !strings.HasPrefix(extLower, ".") {
		extLower = "." + extLower
	}
	newName := strings.Join(nameParts, "_") + extLower
	if newName == oldName {
		reasonParts = append(reasonParts, "无需修改")
	}
	return RenameSuggestion{
		OldName: oldName,
		NewName: newName,
		Reason:  strings.Join(reasonParts, "、"),
	}
}

// ==================== AI 引擎 ====================

type aiRenameResp struct {
	NewName string `json:"new"`
	Reason  string `json:"reason"`
}

type aiChatResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// aiRenameBatch 调 LLM 批量生成建议（分批 20 + 并发 4，失败回退规则）
func aiRenameBatch(ctx context.Context, suggestions []RenameSuggestion) []RenameSuggestion {
	apiURL := setting.GetStr(conf.AiRenameApiUrl)
	apiKey := setting.GetStr(conf.AiRenameApiKey)
	model := setting.GetStr(conf.AiRenameModel, "deepseek-v4-flash")
	if apiURL == "" || apiKey == "" {
		return nil
	}
	type batchJob struct {
		start  int
		names  []string
		result []RenameSuggestion
	}
	const batchSize = 20
	var jobs []batchJob
	for i := 0; i < len(suggestions); i += batchSize {
		end := i + batchSize
		if end > len(suggestions) {
			end = len(suggestions)
		}
		batch := suggestions[i:end]
		names := make([]string, 0, len(batch))
		for _, s := range batch {
			names = append(names, s.OldName)
		}
		jobs = append(jobs, batchJob{start: i, names: names})
	}
	sem := make(chan struct{}, 4)
	done := make(chan batchJob, len(jobs))
	for j := range jobs {
		job := jobs[j]
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			renamed, ok := aiRenameOneBatch(ctx, apiURL, apiKey, model, job.names)
			results := make([]RenameSuggestion, 0, len(job.names))
			for idx, s := range suggestions[job.start : job.start+len(job.names)] {
				if ok && idx < len(renamed) && renamed[idx] != "" {
					s.NewName = renamed[idx]
					s.Reason = "AI 智能命名"
				}
				results = append(results, s)
			}
			job.result = results
			done <- job
		}()
	}
	results := make([]RenameSuggestion, len(suggestions))
	for range jobs {
		job := <-done
		copy(results[job.start:], job.result)
	}
	return results
}

func aiRenameOneBatch(ctx context.Context, apiURL, apiKey, model string, names []string) ([]string, bool) {
	var sb strings.Builder
	for i, n := range names {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, n))
	}
	prompt := `你是一个文件重命名助手。下面是一批视频文件名（可能来自网盘/Telegram，包含 MD5 前缀、广告词、标签等杂乱信息）。
请为每个文件生成一个简洁、可读、去广告、保留内容特征的新文件名（保留原扩展名，文件名用下划线分隔词语，不超过 40 字符，不要序号，不要重复）。
只做文件命名整理，不评价内容。严格按 JSON 数组输出，格式：[{"new":"新文件名","reason":"一句话说明"}]，与输入一一对应，不要输出其他内容。
文件列表：
` + sb.String()

	body, _ := json.Marshal(map[string]any{
		"model":           model,
		"messages":        []map[string]string{{"role": "user", "content": prompt}},
		"temperature":     0.3,
		"response_format": map[string]string{"type": "json_object"},
	})
	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		strings.TrimSuffix(apiURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Warnf("ai rename request failed: %v", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		log.Warnf("ai rename http %d: %s", resp.StatusCode, string(data))
		return nil, false
	}
	var chat aiChatResp
	if err := json.NewDecoder(resp.Body).Decode(&chat); err != nil || len(chat.Choices) == 0 {
		return nil, false
	}
	content := chat.Choices[0].Message.Content
	// 提取 JSON 数组
	start := strings.Index(content, "[")
	end := strings.LastIndex(content, "]")
	if start == -1 || end == -1 || end <= start {
		return nil, false
	}
	var parsed []aiRenameResp
	if err := json.Unmarshal([]byte(content[start:end+1]), &parsed); err != nil {
		return nil, false
	}
	// 校验扩展名与数量
	results := make([]string, 0, len(names))
	used := map[string]bool{}
	for i, p := range parsed {
		if i >= len(names) {
			break
		}
		ext := utils.Ext(names[i])
		newName := strings.TrimSpace(p.NewName)
		if newName == "" {
			results = append(results, "")
			continue
		}
		if !strings.HasSuffix(strings.ToLower(newName), strings.ToLower(ext)) {
			newName += ext
		}
		// 防重名
		finalName := newName
		for n := 1; used[finalName]; n++ {
			finalName = fmt.Sprintf("%s(%d)%s", strings.TrimSuffix(newName, ext), n, ext)
		}
		used[finalName] = true
		results = append(results, finalName)
	}
	if len(results) < len(names) {
		for i := len(results); i < len(names); i++ {
			results = append(results, "")
		}
	}
	return results, true
}

// ==================== API ====================

// RenamePreviewReq POST /api/fs/rename_preview
type RenamePreviewReq struct {
	SrcDir    string `json:"src_dir" binding:"required"`
	Mode      string `json:"mode"`       // rule / ai / auto
	OnlyVideo bool   `json:"only_video"` // 默认 true，只处理视频
}

// FsRenamePreview 预览智能重命名结果（不执行）
func FsRenamePreview(c *gin.Context) {
	var req RenamePreviewReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if req.Mode == "" {
		req.Mode = "auto"
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if !user.CanRename() {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	reqPath, err := user.JoinPath(req.SrcDir)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		common.ErrorResp(c, err, 500, true)
		return
	}
	if !common.CanWrite(user, meta, reqPath) {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	objs, err := fs.List(c.Request.Context(), reqPath, &fs.ListArgs{})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	var suggestions []RenameSuggestion
	for _, obj := range objs {
		if obj.IsDir() {
			continue
		}
		if req.OnlyVideo && utils.GetFileType(obj.GetName()) != conf.VIDEO {
			continue
		}
		suggestions = append(suggestions, ruleRenameName(obj.GetName()))
	}

	// AI 模式
	if req.Mode == "ai" || req.Mode == "auto" {
		if ai := aiRenameBatch(c.Request.Context(), suggestions); ai != nil {
			suggestions = ai
		} else if req.Mode == "ai" {
			// AI 失败回退规则
		}
	}

	// 预览内去重：新名冲突自动加 (n)
	used := map[string]bool{}
	for i := range suggestions {
		ext := utils.Ext(suggestions[i].NewName)
		dotExt := ext
		if dotExt != "" && !strings.HasPrefix(dotExt, ".") {
			dotExt = "." + dotExt
		}
		base := strings.TrimSuffix(suggestions[i].NewName, dotExt)
		name := suggestions[i].NewName
		for n := 1; used[name]; n++ {
			name = fmt.Sprintf("%s(%d)%s", base, n, dotExt)
		}
		used[name] = true
		suggestions[i].NewName = name
		if suggestions[i].NewName == suggestions[i].OldName {
			suggestions[i].Reason = "无需修改"
		}
	}

	// 排序：需要修改的在前
	sort.SliceStable(suggestions, func(i, j int) bool {
		ci := suggestions[i].NewName == suggestions[i].OldName
		cj := suggestions[j].NewName == suggestions[j].OldName
		return ci != cj && !ci
	})
	common.SuccessResp(c, gin.H{"content": suggestions})
}
