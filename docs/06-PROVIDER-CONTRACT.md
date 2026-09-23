# 06 — Provider Contract

业务层不理解第三方专有 API。

统一接口：
Health、Capabilities、ListImages、Create/Get、Start/Stop/Restart/Reinstall/ResetPassword/Delete、Usage/Traffic、PortForward。

Capability 驱动 UI/业务，禁止散落 provider 类型判断。

统一错误：
PROVIDER_TIMEOUT、PROVIDER_UNAVAILABLE、PROVIDER_AUTH_FAILED、NODE_OFFLINE、RESOURCE_EXHAUSTED、IMAGE_NOT_FOUND、INSTANCE_NOT_FOUND、INSTANCE_ALREADY_EXISTS、PORT_EXHAUSTED、NETWORK_ERROR、UNSUPPORTED_OPERATION、UNKNOWN_PROVIDER_ERROR。

Create 必须幂等；timeout 后 Verify。

Direct Provider：LXDAPI/CLICD 等。  
Agent Provider：Runman Gateway → runman-agent。

实现前核对第三方当前官方文档/源码；只改 Adapter，不改 Business Contract。
