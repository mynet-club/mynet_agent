package mynet

import "fmt"

// allCloudNodes follows the API paginator instead of confusing cached profiles
// with the user's inventory. It never follows a server-supplied foreign URL.
func allCloudNodes(c *backClient) ([]any, error) {
	rows := []any{}
	for page := 1; page <= 1000; page++ {
		var raw map[string]any
		if err := c.getEnveloped(fmt.Sprintf("/api/v1/nodes?page=%d&per_page=100", page), &raw); err != nil {
			return nil, err
		}
		p := raw
		if n, ok := raw["nodes"].(map[string]any); ok {
			p = n
		}
		items, ok := p["data"].([]any)
		if !ok {
			return nil, fmt.Errorf("节点列表响应格式不正确")
		}
		rows = append(rows, items...)
		last, _ := p["last_page"].(float64)
		next, _ := p["next_page_url"].(string)
		if last <= float64(page) && next == "" {
			return rows, nil
		}
	}
	return nil, fmt.Errorf("节点列表分页超过上限，未返回部分结果")
}

func configFaceCloud() int {
	c := LoadCredential()
	if c == nil || c.Token == "" {
		return printJSON(map[string]any{"need_login": true, "error": "need_login"})
	}
	rows, err := allCloudNodes(newBackClientFor(c))
	if err != nil {
		return printJSON(map[string]any{"error": err.Error()})
	}
	return printJSON(map[string]any{"nodes": rows, "total": len(rows)})
}
