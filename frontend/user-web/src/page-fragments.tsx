/**
 * Shared page fragments: the page frame, the money/date renders and the
 * state helpers every page uses for its six states (docs/10).
 */

import { Card } from "@vps/shared";
import type { ReactNode } from "react";

export function Page({ title, subtitle, children }: { title: ReactNode; subtitle?: ReactNode; children: ReactNode }): ReactNode {
  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-lg font-semibold text-content">{title}</h1>
        {subtitle !== undefined && <p className="text-sm text-content-muted">{subtitle}</p>}
      </div>
      {children}
    </div>
  );
}


export function Stat({ label, value }: { label: ReactNode; value: ReactNode }): ReactNode {
  return (
    <Card title={label}>
      <p className="text-xl font-semibold text-content">{value}</p>
    </Card>
  );
}

