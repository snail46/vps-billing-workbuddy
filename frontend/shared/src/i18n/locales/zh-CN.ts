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
    // The authentication failures. They are separate from the transport codes above
    // because they say something more specific: `unauthorized` means the session is gone
    // and the user should sign in again, while `invalid_credentials` means the password
    // just entered does not match. Showing the first for the second would send someone
    // looking for a session they never had.
    invalid_credentials: "邮箱或密码不正确。",
    account_suspended: "该账号已被停用，请联系客服。",
    email_taken: "该邮箱已被注册，请直接登录或更换邮箱。",
    invalid_email: "请输入有效的邮箱地址。",
    invalid_password: "密码不符合要求，请设置更长的密码。",
    invalid_locale: "暂不支持该语言。",
    // The commerce failures. They say what went wrong rather than only that something
    // did, because a payment that silently failed is worse than one that says why.
    signature_invalid: "请求签名无效，已拒绝该回调。",
    malformed_callback: "回调内容无法解析。",
    order_not_payable: "当前订单状态不支持付款，请刷新后查看。",
    payment_already_started: "该订单已发起过付款，请勿重复提交。",
    payment_amount_mismatch: "回调金额与订单金额不一致，已拒绝入账。",
    payment_currency_mismatch: "回调币种与订单币种不一致，已拒绝入账。",
    unsupported_notification: "暂不支持处理该类型的回调。",
    invalid_plan: "所选套餐不存在或已下架。",
    plan_not_purchaseable: "所选套餐当前不可购买。",
    mixed_currencies: "一份订单只能使用一种货币。",
        empty_order: "请至少选择一件商品。",
    subscription_not_live: "该订阅已结束，无法继续操作。",
    nothing_due: "当前周期尚未到期，暂时无需续费。",
    insufficient_balance: "余额不足以支付本次续费，请先充值。",
  },
  operation: {
    status: {
      queued: "排队中",
      running: "执行中",
      waiting_provider: "等待服务商",
      waiting_resource: "等待资源",
      verifying: "验证中",
      retrying: "等待重试",
      succeeded: "已完成",
      failed: "已失败",
      cancelled: "已取消",
    },
    step: {
      pending: "待执行",
      running: "执行中",
      succeeded: "已完成",
      failed: "已失败",
      skipped: "已跳过",
      attempt: "第 {{count}} 次尝试",
    },
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
