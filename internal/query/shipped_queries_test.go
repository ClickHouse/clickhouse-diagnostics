package query

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 每个随包发布的查询文件都必须通过 executor 在发送前运行的同一项检查。
// executor 会拒绝未通过检查的文件 —— 这发生在采集时、在客户的服务器上，
// 唯一的痕迹只是一行 "Error executing query" 和 bundle 中缺失的文件。
// 上面那些内联抽查无法捕获这种情况：一个字符串字面量（'Create'）中提到的
// Keeper 操作曾在一个新 collector 中触发了 CREATE 规则，而只有实验室运行
// 才发现了它。
func TestShippedQueryFiles_PassTheSafetyCheck(t *testing.T) {
	roots := []string{"../../queries.onprem", "../../queries.cloud", "../../queries.gov", "../../queries.query_analysis"}
	checked := 0
	for _, root := range roots {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			t.Fatalf("%s 不存在", root)
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sql") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			checked++
			if verr := ValidateQueryContent(string(b)); verr != nil {
				t.Errorf("%s: %v", strings.TrimPrefix(path, "../../"), verr)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked < 100 {
		t.Fatalf("只检查了 %d 个查询文件 —— 遍历没有到达随包发布的目录树", checked)
	}
}
