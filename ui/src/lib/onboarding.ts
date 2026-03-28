import { api } from "./api";
import type { OnboardingStatus } from "../types";

export function fetchOnboardingStatus(): Promise<OnboardingStatus> {
  return api.get<OnboardingStatus>("/onboarding/status");
}
