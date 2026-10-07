"use client"

import * as React from "react"
import {
  BanIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  FolderIcon,
  FolderPlusIcon,
  Loader2Icon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  SearchIcon,
  Trash2Icon,
} from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { adminApi, formatDate } from "@/lib/api"

import { LinkCell } from "./link-status"
import {
  CopyButton,
  InlineError,
  PanelStatus,
  statusLabels,
  statusVariant,
  type CodeRow,
  type ListResponse,
} from "./shared"

function ConfirmDialog({
  open,
  title,
  description,
  confirmLabel,
  onCancel,
  onConfirm,
}: {
  open: boolean
  title: string
  description: string
  confirmLabel: string
  onCancel: () => void
  onConfirm: () => void
}) {
  return (
    <Dialog open={open} onOpenChange={(value) => (!value ? onCancel() : undefined)}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>
            取消
          </Button>
          <Button variant="destructive" onClick={onConfirm}>
            {confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function CodesPanel({ password }: { password: string }) {
  const [data, setData] = React.useState<ListResponse | null>(null)
  const [page, setPage] = React.useState(0)
  const [folder, setFolder] = React.useState("all")
  const [loading, setLoading] = React.useState(false)
  const [error, setError] = React.useState("")
  const [notice, setNotice] = React.useState("")
  const [generateOpen, setGenerateOpen] = React.useState(false)
  const [foldersOpen, setFoldersOpen] = React.useState(false)
  const [revokeTarget, setRevokeTarget] = React.useState<CodeRow | null>(null)
  const [lookupCode, setLookupCode] = React.useState("")
  const [lookupResult, setLookupResult] = React.useState<CodeRow | null>(null)
  const [lookupError, setLookupError] = React.useState("")

  // generate form
  const [months, setMonths] = React.useState("3")
  const [count, setCount] = React.useState("10")
  const [batch, setBatch] = React.useState("")
  const [generated, setGenerated] = React.useState<string[]>([])

  // folders
  const [newFolder, setNewFolder] = React.useState("")
  const [renaming, setRenaming] = React.useState<{ id: string; name: string } | null>(null)

  const load = React.useCallback(async () => {
    setLoading(true)
    setError("")
    const query = new URLSearchParams({ page: String(page) })
    if (folder !== "all") query.set("folder", folder)
    const result = await adminApi<ListResponse>(password, `/api/admin/codes?${query.toString()}`)
    setLoading(false)
    if (!result.ok) {
      setError(result.message || "无法读取兑换码列表。")
      return
    }
    setData(result.data)
  }, [password, page, folder])

  React.useEffect(() => {
    void load()
  }, [load])

  const folders = data?.folders ?? []
  const stats = data?.stats ?? {}

  async function submitGenerate(event: React.FormEvent) {
    event.preventDefault()
    setError("")
    const result = await adminApi<{ codes: string[]; batch: string }>(password, "/api/admin/codes", {
      months: Number(months),
      count: Number(count),
      batch: batch.trim(),
      folder: folder !== "all" && folder !== "unfiled" ? folder : "",
    })
    if (!result.ok) {
      setError(result.message || "生成失败。")
      return
    }
    setGenerated(result.data.codes ?? [])
    setBatch("")
    void load()
  }

  async function revoke() {
    if (!revokeTarget) return
    const result = await adminApi<{ message: string }>(password, "/api/admin/revoke", {
      id: revokeTarget.id,
    })
    setRevokeTarget(null)
    if (!result.ok) {
      setError(result.message || "停用失败。")
      return
    }
    setNotice("兑换码已停用。")
    void load()
  }

  async function copyCode(row: CodeRow) {
    const result = await adminApi<{ code: string }>(password, "/api/admin/codes/copy", { id: row.id })
    if (!result.ok || !result.data.code) {
      setError(result.message || "该历史兑换码未保存完整内容。")
      return
    }
    try {
      await navigator.clipboard.writeText(result.data.code)
      setNotice(`已复制尾号 ${row.hint} 的完整兑换码。`)
    } catch {
      setError("浏览器拒绝访问剪贴板。")
    }
  }

  async function lookup(event: React.FormEvent) {
    event.preventDefault()
    setLookupError("")
    setLookupResult(null)
    const result = await adminApi<CodeRow>(password, "/api/admin/lookup", { code: lookupCode.trim() })
    if (!result.ok) {
      setLookupError(result.message || "未找到兑换码。")
      return
    }
    setLookupResult(result.data)
  }

  async function createFolder(event: React.FormEvent) {
    event.preventDefault()
    const result = await adminApi<{ id: string }>(password, "/api/admin/folders", {
      name: newFolder.trim(),
    })
    if (!result.ok) {
      setError(result.message || "创建文件夹失败。")
      return
    }
    setNewFolder("")
    void load()
  }

  async function renameFolder(event: React.FormEvent) {
    event.preventDefault()
    if (!renaming) return
    const result = await adminApi<{ message: string }>(password, "/api/admin/folders/rename", {
      id: renaming.id,
      name: renaming.name.trim(),
    })
    if (!result.ok) {
      setError(result.message || "重命名失败。")
      return
    }
    setRenaming(null)
    void load()
  }

  async function deleteFolder(id: string) {
    const result = await adminApi<{ message: string }>(password, "/api/admin/folders/delete", { id })
    if (!result.ok) {
      setError(result.message || "删除失败。")
      return
    }
    setNotice("文件夹已删除，兑换码已移至未分类。")
    if (folder === id) setFolder("all")
    else void load()
  }

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={() => setGenerateOpen(true)}>
          <PlusIcon />
          生成兑换码
        </Button>
        <Button variant="outline" onClick={() => setFoldersOpen(true)}>
          <FolderIcon />
          管理文件夹
        </Button>
        <div className="ml-auto flex items-center gap-2">
          <Select value={folder} onValueChange={(value) => { setFolder(value as string); setPage(0) }}>
            <SelectTrigger className="w-44" aria-label="筛选文件夹">
              <SelectValue placeholder="全部兑换码" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部兑换码</SelectItem>
              <SelectItem value="unfiled">未分类</SelectItem>
              {folders.map((item) => (
                <SelectItem key={item.id} value={item.id}>
                  {item.name}（{item.count}）
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button variant="outline" size="icon" aria-label="刷新" onClick={() => void load()}>
            {loading ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
          </Button>
        </div>
      </div>

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        {[
          { key: "total", label: "总数" },
          { key: "active", label: "未使用" },
          { key: "processing", label: "处理中" },
          { key: "succeeded", label: "已完成" },
        ].map((item) => (
          <div key={item.key} className="rounded-xl border border-border p-3">
            <p className="text-xs text-muted-foreground">{item.label}</p>
            <p className="mt-1 text-xl font-semibold tabular-nums">{stats[item.key] ?? 0}</p>
          </div>
        ))}
      </div>

      <InlineError>{error}</InlineError>
      <PanelStatus>{notice}</PanelStatus>

      <Card className="overflow-hidden p-0">
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>尾号</TableHead>
                <TableHead>批次</TableHead>
                <TableHead>套餐</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>用户名</TableHead>
                <TableHead>付款链接</TableHead>
                <TableHead>创建时间</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(data?.codes ?? []).map((row) => (
                <TableRow key={row.id}>
                  <TableCell className="font-mono text-xs">{row.hint}</TableCell>
                  <TableCell className="max-w-40 truncate">{row.batch || "—"}</TableCell>
                  <TableCell>{row.months} 个月</TableCell>
                  <TableCell>
                    <Badge variant={statusVariant(row.status)}>{statusLabels[row.status] ?? row.status}</Badge>
                  </TableCell>
                  <TableCell>{row.username ? `@${row.username}` : "—"}</TableCell>
                  <TableCell>
                    <LinkCell row={row} />
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">{formatDate(row.created)}</TableCell>
                  <TableCell>
                    <div className="flex items-center justify-end gap-1">
                      <CopyButton
                        value={row.checkout_url ?? ""}
                        label="链接"
                        disabled={!row.checkout_url}
                      />
                      <Button
                        variant="ghost"
                        size="xs"
                        disabled={!row.copyable}
                        onClick={() => void copyCode(row)}
                      >
                        卡密
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        disabled={row.status !== "active" && row.status !== "processing"}
                        onClick={() => setRevokeTarget(row)}
                      >
                        <BanIcon />
                        停用
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
              {data && data.codes.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={8} className="py-10 text-center text-sm text-muted-foreground">
                    当前筛选下还没有兑换码。
                  </TableCell>
                </TableRow>
              ) : null}
            </TableBody>
          </Table>
        </div>
      </Card>

      <div className="flex items-center justify-between">
        <p className="text-xs text-muted-foreground">
          第 {page + 1} 页 · 每页最多 100 条
        </p>
        <div className="flex gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={page === 0 || loading}
            onClick={() => setPage((value) => Math.max(0, value - 1))}
          >
            <ChevronLeftIcon />
            上一页
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={!data?.has_more || loading}
            onClick={() => setPage((value) => value + 1)}
          >
            下一页
            <ChevronRightIcon />
          </Button>
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">兑换码查询</CardTitle>
          <CardDescription>输入完整兑换码，查看绑定账号、状态与付款链接。</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-3">
          <form className="flex flex-col gap-2 sm:flex-row" onSubmit={lookup}>
            <Input
              value={lookupCode}
              onChange={(event) => setLookupCode(event.target.value.toUpperCase())}
              placeholder="XG-...（完整 51 位）"
              className="font-mono text-xs"
              aria-label="完整兑换码"
            />
            <Button type="submit" variant="outline">
              <SearchIcon />
              查询
            </Button>
          </form>
          <InlineError>{lookupError}</InlineError>
          {lookupResult ? (
            <div className="grid gap-2 rounded-lg border border-border p-3 text-sm">
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant={statusVariant(lookupResult.status)}>
                  {statusLabels[lookupResult.status] ?? lookupResult.status}
                </Badge>
                <span>{lookupResult.username ? `@${lookupResult.username}` : "未绑定账号"}</span>
                <span className="text-muted-foreground">{lookupResult.months} 个月</span>
              </div>
              <p className="text-muted-foreground">{lookupResult.message || "—"}</p>
              {lookupResult.checkout_url ? (
                <a
                  className="text-xs text-primary underline-offset-4 hover:underline"
                  href={lookupResult.checkout_url}
                  target="_blank"
                  rel="noreferrer noopener"
                >
                  {lookupResult.checkout_url}
                </a>
              ) : null}
            </div>
          ) : null}
        </CardContent>
      </Card>

      <Dialog open={generateOpen} onOpenChange={setGenerateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>生成兑换码</DialogTitle>
            <DialogDescription>
              一次最多生成 500 枚；批次名称留空会自动生成。
            </DialogDescription>
          </DialogHeader>
          <form className="grid gap-4" onSubmit={submitGenerate}>
            <div className="grid grid-cols-2 gap-4">
              <div className="grid gap-2">
                <Label>套餐时长</Label>
                <Select value={months} onValueChange={(value) => setMonths(value as string)}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="3">3 个月</SelectItem>
                    <SelectItem value="6">6 个月</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="grid gap-2">
                <Label htmlFor="count">数量</Label>
                <Input
                  id="count"
                  type="number"
                  min={1}
                  max={500}
                  value={count}
                  onChange={(event) => setCount(event.target.value)}
                />
              </div>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="batch">批次名称（可选）</Label>
              <Input id="batch" value={batch} maxLength={120} onChange={(event) => setBatch(event.target.value)} />
            </div>
            {generated.length > 0 ? (
              <div className="grid gap-2 rounded-lg border border-border p-3">
                <div className="flex items-center justify-between">
                  <p className="text-sm font-medium">已生成 {generated.length} 枚</p>
                  <CopyButton value={generated.join("\n")} label="复制全部" />
                </div>
                <textarea
                  readOnly
                  value={generated.join("\n")}
                  className="h-28 w-full resize-none rounded-md border border-input bg-transparent p-2 font-mono text-xs"
                />
              </div>
            ) : null}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setGenerateOpen(false)}>
                关闭
              </Button>
              <Button type="submit" disabled={!count || Number(count) < 1}>
                生成
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={foldersOpen} onOpenChange={setFoldersOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>文件夹管理</DialogTitle>
            <DialogDescription>删除文件夹不会删除兑换码，内容会移至未分类。</DialogDescription>
          </DialogHeader>
          <form className="flex gap-2" onSubmit={createFolder}>
            <Input
              value={newFolder}
              maxLength={120}
              onChange={(event) => setNewFolder(event.target.value)}
              placeholder="新文件夹名称"
              aria-label="新文件夹名称"
            />
            <Button type="submit" disabled={!newFolder.trim()}>
              <FolderPlusIcon />
              创建
            </Button>
          </form>
          <div className="grid max-h-72 gap-2 overflow-y-auto">
            {folders.length === 0 ? (
              <p className="py-6 text-center text-sm text-muted-foreground">还没有文件夹。</p>
            ) : null}
            {folders.map((item) =>
              renaming?.id === item.id ? (
                <form key={item.id} className="flex gap-2" onSubmit={renameFolder}>
                  <Input
                    autoFocus
                    value={renaming.name}
                    onChange={(event) => setRenaming({ id: item.id, name: event.target.value })}
                    aria-label="新名称"
                  />
                  <Button type="submit" size="sm">
                    保存
                  </Button>
                  <Button type="button" size="sm" variant="outline" onClick={() => setRenaming(null)}>
                    取消
                  </Button>
                </form>
              ) : (
                <div key={item.id} className="flex items-center gap-2 rounded-lg border border-border p-2">
                  <span className="flex-1 truncate text-sm">{item.name}</span>
                  <span className="text-xs text-muted-foreground">{item.count} 枚</span>
                  <Button
                    variant="ghost"
                    size="icon-xs"
                    aria-label={`重命名 ${item.name}`}
                    onClick={() => setRenaming({ id: item.id, name: item.name })}
                  >
                    <PencilIcon />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-xs"
                    aria-label={`删除 ${item.name}`}
                    onClick={() => void deleteFolder(item.id)}
                  >
                    <Trash2Icon />
                  </Button>
                </div>
              ),
            )}
          </div>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={revokeTarget !== null}
        title="停用兑换码"
        description={`确定停用尾号 ${revokeTarget?.hint ?? ""} 的兑换码吗？停用后无法恢复。`}
        confirmLabel="停用"
        onCancel={() => setRevokeTarget(null)}
        onConfirm={() => void revoke()}
      />
    </div>
  )
}
