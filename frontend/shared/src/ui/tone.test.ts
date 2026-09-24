import { describe, expect, it } from "vitest";

import {
  operationStatusLabelKey,
  operationStatusTone,
} from "./tone.js";

// The mapping the OperationProgress component renders by: a state the machine
// knows must have a tone and a label key, because a state that renders as a raw
// key or an untoned badge is the exact defect this table exists to prevent.
const ALL_STATUSES = [
  "queued",
  "running",
  "waiting_provider",
  "waiting_resource",
  "verifying",
  "retrying",
  "succeeded",
  "failed",
  "cancelled",
] as const;

const VALID_TONES = new Set(["success", "info", "warning", "danger", "neutral"]);

describe("operationStatusTone", () => {
  it("maps every machine state to a valid tone", () => {
    for (const status of ALL_STATUSES) {
      expect(VALID_TONES.has(operationStatusTone(status)), status).toBe(true);
    }
  });

  it("reserves danger for failure and success for completion", () => {
    expect(operationStatusTone("failed")).toBe("danger");
    expect(operationStatusTone("succeeded")).toBe("success");
    expect(operationStatusTone("cancelled")).toBe("neutral");
  });

  it("reads the waiting states as warnings, not errors", () => {
    expect(operationStatusTone("waiting_provider")).toBe("warning");
    expect(operationStatusTone("waiting_resource")).toBe("warning");
    expect(operationStatusTone("retrying")).toBe("warning");
  });
});

describe("operationStatusLabelKey", () => {
  it("keys every state into the operation namespace", () => {
    for (const status of ALL_STATUSES) {
      expect(operationStatusLabelKey(status)).toBe(`operation.status.${status}`);
    }
  });
});
