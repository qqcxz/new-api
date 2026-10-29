# Fork 维护指南：固定在一个分支开发

## 日常只使用一个分支

所有自定义功能和修复都放在 **`feat/custom-development`**。不需要每次找 AI Agent 都创建分支，也不要求每个功能完成后再合入 `dev`。

- `feat/custom-development`：唯一日常开发分支，所有个人功能持续积累在这里。
- `dev`：保存本次维护准备的起点；日常无需切换到它。
- `main`：官方代码基线，不放个人功能。
- `sync/upstream-*`：只有更新官方代码时使用的临时分支，由 Agent 处理；不是新的功能开发分支。

多个功能在同一分支开发没有问题。为了以后方便定位、撤回某项功能，提交时仍尽量按功能拆成独立 commit。一个功能一个 commit，不等于一个功能一个分支。

## 给 AI Agent 的描述

根目录 `AGENTS.md` 已记录固定分支约定。可以直接这样描述需求：

> 请在当前 `feat/custom-development` 分支增加【功能名称】，继续使用这个分支，不要另建或切换功能分支。
>
> 需求：【描述希望用户能做什么、入口在哪、输入输出和边界情况】。
> 验收标准：【列出完成后应该看到的行为】。
>
> 先阅读 `AGENTS.md` 和相关目录约定，复用现有实现并保留我已有的修改。完成后运行相关检查，汇报改动和测试结果；不要自行推送 GitHub，也不要顺带合并官方更新。

如果希望 Agent 同时提交，再补一句：“验证通过后，只把本次功能相关改动提交成一个独立 commit，不要推送。”

同一份工作目录一次让一个 Agent 修改代码，避免同时编辑同一个分支的文件。

## 检查和保存工作

```bash
git branch --show-current
node fork/maintain.mjs status
```

应当显示 `feat/custom-development`。开发后执行与修改范围相符的项目测试，再按需提交。工具不会自动提交或推送。需要首次备份到自己的 GitHub Fork 时，明确推送这个分支：

```bash
git push -u origin feat/custom-development
```

之后可用普通 `git push` 保存已提交的更新。新设备应克隆此分支，或在 GitHub 将 Fork 默认分支改成它：

```bash
git clone --branch feat/custom-development https://github.com/<你的 GitHub 用户名>/new-api.git
cd new-api
node fork/maintain.mjs setup
```

`setup` 默认恢复或创建 `feat/custom-development`。已有本地分支不重置；远端存在时跟踪 `origin/feat/custom-development`；否则优先从准备分支 `dev` 创建。需要 Git 和 Node.js 22 或更新。

维护脚本只设置当前仓库的 Git 配置：默认推送 `origin`、`pull.ff=only`、`rerere`，并禁用向官方 `upstream` 的推送地址。官方抓取地址仍保留。`setup dev` 是兼容旧流程的命令，日常不需要使用。

## 合并官方更新

以后直接让 Agent：“在保留所有个人功能的前提下，帮我合并官方更新并验证，最后回到 `feat/custom-development`。”你无需自己记住临时分支名。

给 Agent 的维护流程：

1. 确认工作目录没有未提交、未跟踪或进行中的合并。先保存自己的改动，不要擅自丢弃用户工作。
2. 在固定开发分支对齐 `origin/feat/custom-development`；首次同步前需要已经推送过这个分支。若尚无 GitHub 推送授权，先完成本地工作并报告，不自动推送。
3. 执行下面的同步命令。它抓取 `upstream/main`，创建临时同步分支并合并，但不提交或推送。

```bash
node fork/maintain.mjs sync
```

4. 解决冲突、检查差异并运行相关测试。确认无误后，在临时分支提交合并。保留官方提交历史，不要 squash 或 rebase。
5. 回到固定分支，通过快进接收已经验证的合并提交。将示例中的临时分支名称替换成工具实际打印的名称。

```bash
git commit -m "chore: sync upstream main"
git switch feat/custom-development
git merge --ff-only sync/upstream-实际时间
```

提交前如有冲突解决文件，需要先审查并 `git add`。如果固定分支已经推进导致不能快进，停止并检查新的提交，不要重置分支。远端备份仍需要用户授权。

若使用 GitHub PR，同步 PR 的目标也应为 `feat/custom-development`，并选择 **Create a merge commit**，保持官方祖先关系。取消正在进行的合并时，在同步分支执行 `git merge --abort`，再切回固定开发分支。

## 检查命令

```bash
node --check fork/maintain.mjs
node --test fork/maintain.test.mjs
git diff --check
```

原有前后端 CI 已支持推送到 `feat/custom-development`，不必为了运行 CI 每次开 PR；GitHub Actions 需要在自己的 Fork 中启用。维护工具的 CI 仅在相关维护文件变化时运行。GitHub 上尚未推送的本地提交不会触发远程 CI。

两个继承的 Docker 工作流已限制为官方仓库执行镜像构建发布，保留官方镜像信息。个人 Fork 要发布自己的镜像时应另外配置。其余 Release/Electron 工作流仍可能在推送标签时创建发布，因此未准备好发布流程前不要推送版本标签，也不要手动触发发布工作流。
