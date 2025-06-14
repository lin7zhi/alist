package thunder

import (
	"context"
	"errors"
	"fmt"
	"github.com/alist-org/alist/v3/internal/conf"
	"github.com/alist-org/alist/v3/internal/setting"
	"strconv"

	"github.com/alist-org/alist/v3/drivers/thunder"
	"github.com/alist-org/alist/v3/internal/errs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/offline_download/tool"
	"github.com/alist-org/alist/v3/internal/op"
)

type Thunder struct {
	refreshTaskCache bool
}

func (t *Thunder) Name() string {
	return "Thunder"
}

func (t *Thunder) Items() []model.SettingItem {
	return nil
}

func (t *Thunder) Run(task *tool.DownloadTask) error {
	return errs.NotSupport
}

func (t *Thunder) Init() (string, error) {
	t.refreshTaskCache = false
	return "ok", nil
}

func (t *Thunder) IsReady() bool {
	tempDir := setting.GetStr(conf.ThunderTempDir)
	if tempDir == "" {
		return false
	}
	storage, _, err := op.GetStorageAndActualPath(tempDir)
	if err != nil {
		return false
	}
	if _, ok := storage.(*thunder.Thunder); ok {
		return true
	}
	if _, ok := storage.(*thunder.ThunderExpert); ok {
		return true
	}
	return false
}

func (t *Thunder) AddURL(args *tool.AddUrlArgs) (string, error) {
	// 添加新任务刷新缓存
	t.refreshTaskCache = true
	storage, actualPath, err := op.GetStorageAndActualPath(args.TempDir)
	if err != nil {
		return "", err
	}
	ctx := context.Background()

	if err := op.MakeDir(ctx, storage, actualPath); err != nil {
		return "", err
	}

	parentDir, err := op.GetUnwrap(ctx, storage, actualPath)
	if err != nil {
		return "", err
	}

	var task *thunder.OfflineTask
	if thunderDriver, ok := storage.(*thunder.Thunder); ok {
		task, err = thunderDriver.OfflineDownload(ctx, args.Url, parentDir, "")
	} else if expertDriver, ok := storage.(*thunder.ThunderExpert); ok {
		task, err = expertDriver.OfflineDownload(ctx, args.Url, parentDir, "")
	} else {
		return "", fmt.Errorf("unsupported storage driver for offline download, only Thunder or Thunder Expert is supported")
	}

	if err != nil {
		return "", fmt.Errorf("failed to add offline download task: %w", err)
	}

	return task.ID, nil
}

func (t *Thunder) Remove(task *tool.DownloadTask) error {
	storage, _, err := op.GetStorageAndActualPath(task.TempDir)
	if err != nil {
		return err
	}
	if thunderDriver, ok := storage.(*thunder.Thunder); ok {
		err = thunderDriver.DeleteOfflineTasks(context.Background(), []string{task.GID}, false)
	} else if expertDriver, ok := storage.(*thunder.ThunderExpert); ok {
		err = expertDriver.DeleteOfflineTasks(context.Background(), []string{task.GID}, false)
	} else {
		return fmt.Errorf("unsupported storage driver for offline download, only Thunder or Thunder Expert is supported")
	}
	return err
}

func (t *Thunder) Status(task *tool.DownloadTask) (*tool.Status, error) {
	storage, _, err := op.GetStorageAndActualPath(task.TempDir)
	if err != nil {
		return nil, err
	}
	var tasks []thunder.OfflineTask
	if thunderDriver, ok := storage.(*thunder.Thunder); ok {
		tasks, err = t.GetTasks(thunderDriver)
	} else if expertDriver, ok := storage.(*thunder.ThunderExpert); ok {
		tasks, err = t.GetTasks(expertDriver)
	} else {
		return nil, fmt.Errorf("unsupported storage driver for offline download, only Thunder or Thunder Expert is supported")
	}
	if err != nil {
		return nil, err
	}
	s := &tool.Status{
		Progress:  0,
		NewGID:    "",
		Completed: false,
		Status:    "the task has been deleted",
		Err:       nil,
	}
	for _, t := range tasks {
		if t.ID == task.GID {
			s.Progress = float64(t.Progress)
			s.Status = t.Message
			s.Completed = (t.Phase == "PHASE_TYPE_COMPLETE")
			s.TotalBytes, err = strconv.ParseInt(t.FileSize, 10, 64)
			if err != nil {
				s.TotalBytes = 0
			}
			if t.Phase == "PHASE_TYPE_ERROR" {
				s.Err = errors.New(t.Message)
			}
			return s, nil
		}
	}
	s.Err = fmt.Errorf("the task has been deleted")
	return s, nil
}

func init() {
	tool.Tools.Add(&Thunder{})
}
