# 07 — Workflow Spec

Provision：
validate_subscription → select_node → reserve_resources → create_instance → wait_provider → configure_network → verify_running → persist_network → commit_reservation → activate_subscription → notify → finish。

Reinstall：
validate → lock → submit → wait → verify → sync_network → update_image → unlock → notify。

Delete：
validate → desired=deleted → provider_delete → verify_not_found → release network/ports/capacity → observed=deleted → audit。

Renew：
invoice → payment → ledger → extend subscription → update due → if suspended set desired running → reconcile。

进度按步骤映射，不按时间伪造。
