/**
 * zh-CN resources.
 *
 * Key structure follows docs/13-I18N-SPEC.md: internal keys are English, and the
 * `errors.*` namespace mirrors the backend's message keys exactly
 * (`errors.not_found` for `NOT_FOUND`), so a failure can be rendered by
 * translating the key the server sent without any mapping table.
 */
export const zhCN = {
  app: {
    name: "VPS 计费平台",
    adminName: "VPS 计费平台 · 管理后台",
    environment: "运行环境",
  },
  common: {
    actions: {
      retry: "重试",
      refresh: "刷新",
    },
    state: {
      loading: "加载中",
      empty: {
        title: "暂无数据",
        description: "当前没有可显示的内容。",
      },
      error: {
        title: "加载失败",
        description: "请稍后重试；若持续失败，请将请求编号提供给客服。",
      },
      partialError: {
        title: "部分数据不可用",
        description: "页面已加载，但部分内容获取失败。",
      },
      permissionDenied: {
        title: "无访问权限",
        description: "当前账号没有查看此内容的权限。",
      },
    },
    requestId: "请求编号",
    unavailable: "不可用",
  },
  errors: {
    internal_error: "服务出现异常，请稍后重试。",
    validation_failed: "提交的内容不合法，请检查后重试。",
    unauthorized: "登录状态已失效，请重新登录。",
    forbidden: "没有执行该操作的权限。",
    not_found: "请求的资源不存在。",
    method_not_allowed: "请求方式不被支持。",
    conflict: "当前状态与请求冲突，请刷新后重试。",
    rate_limited: "操作过于频繁，请稍后重试。",
    request_timeout: "处理超时，请稍后重试。",
    service_unavailable: "服务暂时不可用，请稍后重试。",
    network_error: "网络连接失败，请检查网络后重试。",
    invalid_response: "服务返回了无法识别的数据。",
    unknown: "发生未知错误，请稍后重试。",
  },
  health: {
    title: "平台状态",
    subtitle: "基础环境依赖的实时可用性。",
    status: {
      up: "正常",
      down: "异常",
    },
    field: {
      version: "版本",
      commit: "提交",
      environment: "环境",
      uptime: "运行时长",
      checkedAt: "检查时间",
    },
    checks: {
      title: "依赖检查",
      empty: "尚未注册任何依赖检查。",
      latency: "{{ms}} 毫秒",
      column: {
        name: "依赖",
        status: "状态",
        latency: "耗时",
        diagnostic: "诊断信息",
      },
    },
    uptime: {
      seconds: "{{count}} 秒",
      minutes: "{{count}} 分钟",
      hours: "{{count}} 小时",
      days: "{{count}} 天",
    },
  },
  foundation: {
    title: "Phase 0 基础环境",
    description:
      "此页面用于验证基础环境：接口调用、错误处理、双语与设计令牌。业务页面将在后续阶段实现。",
    scope: "当前范围",
    scopeItems: {
      api: "接口连通与统一响应信封",
      i18n: "zh-CN / en-US 双语与语言切换",
      states: "loading / empty / error / partial_error / permission_denied",
      tokens: "设计系统语义色与状态徽标",
    },
  },
  locale: {
    label: "语言",
    "zh-CN": "简体中文",
    "en-US": "English",
  },
} as const;
