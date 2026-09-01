package proxies

import (
	"fmt"
	"github.com/sinspired/subs-check-pro/v2/config"
	"github.com/sinspired/subs-check-pro/v2/save/method"
	"github.com/sinspired/subs-check-pro/v2/utils"
	"log/slog"
	"os"
	"path/filepath"
)

// initEnvironment 初始化代理环境变量
func initEnvironment() {
	saver, err := method.NewLocalSaver()
	if err == nil {
		srcDir := saver.OutputPath
		targetDir := filepath.Join(saver.OutputPath, "sub")
		saver.OutputPath = targetDir
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			slog.Error("创建 sub 目录失败", "error", err)
		}
		migrateAndRemove(srcDir, "history.yaml", targetDir)
		migrateAndRemove(srcDir, "all.yaml", targetDir)
		migrateAndRemove(srcDir, "mihomo.yaml", targetDir)
		migrateAndRemove(srcDir, "base64.txt", targetDir)
	}

	slog.Info("获取系统代理和Github代理状态")
	utils.IsSysProxyAvailable = utils.GetSysProxy()
	utils.IsGhProxyAvailable = utils.GetGhProxy()
	if utils.IsSysProxyAvailable {
		slog.Info("", "-system-proxy", config.GlobalConfig.SystemProxy)
	}
	if utils.IsGhProxyAvailable {
		slog.Info("", "-github-proxy", config.GlobalConfig.GithubProxy)
	}
}

// migrateOldFiles 迁移旧文件
func migrateOldFiles(srcDir, fileName, targetDir string) error {
	src := filepath.Join(srcDir, fileName)
	dst := filepath.Join(targetDir, fileName)

	// 目标已存在 -> 不做任何操作
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查目标文件失败: %w", err)
	}

	// 源不存在 -> 不做任何操作
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("检查源文件失败: %w", err)
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("读取源文件失败: %w", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("写入目标文件失败: %w", err)
	}
	return nil
}

// migrateAndRemove 尝试迁移旧文件到 targetDir，成功后删除源文件
func migrateAndRemove(srcDir, fileName, targetDir string) {
	if err := migrateOldFiles(srcDir, fileName, targetDir); err == nil {
		if err := os.Remove(filepath.Join(srcDir, fileName)); err != nil && !os.IsNotExist(err) {
			slog.Warn("删除旧文件失败", "file", fileName, "error", err)
		}
	} else {
		slog.Info("迁移出错", "file", fileName, "error", err, "srcDir", srcDir, "targetDir", targetDir)
	}
}
