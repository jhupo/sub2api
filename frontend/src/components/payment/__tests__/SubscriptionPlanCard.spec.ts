import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import { createPinia } from "pinia";
import { createI18n } from "vue-i18n";
import type { SubscriptionPlan } from "@/types/payment";
import type { UserSubscription } from "@/types";
import { formatDateTimeToMinute } from "@/utils/format";
import SubscriptionPlanCard from "../SubscriptionPlanCard.vue";

const i18n = createI18n({
  legacy: false,
  locale: "en",
  fallbackWarn: false,
  missingWarn: false,
  messages: {
    en: {
      payment: {
        days: "days",
        weeks: "weeks",
        months: "months",
        perMonth: "month",
        models: "Models",
        planCard: {
          quota: "Quota",
          rate: "Rate",
          unlimited: "Unlimited",
        },
        subscribeNow: "Subscribe now",
      },
    },
  },
});

const mountPlanCard = (overrides: Partial<SubscriptionPlan> = {}) =>
  mount(SubscriptionPlanCard, {
    props: {
      plan: {
        id: 1,
        name: "Pro",
        description: "",
        price: 10,
        features: [],
        validity_days: 30,
        validity_unit: "day",
        for_sale: true,
        sort_order: 1,
        ...overrides,
      },
    },
    global: { plugins: [i18n, createPinia()] },
  });

describe("SubscriptionPlanCard", () => {
  it("shows the exact deadline and lifetime quota instead of one day and unlimited", () => {
    const deadline = new Date(2027, 0, 2, 18, 30).toISOString();
    const wrapper = mountPlanCard({card_mode: "deadline", deadline_at: deadline, total_limit_usd: 300, validity_days: 1});

    expect(wrapper.get("time").attributes("datetime")).toBe(deadline);
    expect(wrapper.get("time").text()).toBe(formatDateTimeToMinute(deadline));
    expect(wrapper.get(".plan-card__limits").text()).toContain("$300");
    expect(wrapper.find(".plan-card__period").exists()).toBe(false);
    expect(wrapper.text()).not.toContain("payment.planCard.unlimited");
  });

  it("shows an hourly card's duration and total allowance", () => {
    const wrapper = mountPlanCard({card_mode: "hours", validity_hours: 12, total_limit_usd: 300, validity_days: 1});
    expect(wrapper.get(".plan-card__period").text()).toContain("12quotaFeatures.hours");
    expect(wrapper.get(".plan-card__limits").text()).toContain("$300");
    expect(wrapper.text()).not.toContain("payment.planCard.unlimited");
  });

  it("keeps repeated quota card purchases separate from subscription renewals", async () => {
    const wrapper = mountPlanCard({card_mode: "hours", validity_hours: 12, total_limit_usd: 300});
    await wrapper.setProps({activeSubscriptions: [{plan_id: 1, status: "active"} as UserSubscription]});
    expect(wrapper.get("button").text()).toBe("payment.subscribeNow");
    expect(wrapper.classes()).not.toContain("plan-card--owned");
  });

  it("renders plan features without deriving vendor-specific model scopes", () => {
    const text = mountPlanCard({ features: ["Shared usage", "Priority support"] }).text();

    expect(text).toContain("Shared usage");
    expect(text).toContain("Priority support");
    expect(text).not.toContain("Claude");
    expect(text).not.toContain("Gemini");
  });

  // #4607：管理端保存的单位是复数（months/weeks），此前用户侧只匹配单数
  // 'month'，「1 个月」的套餐卡片被显示成「1天」。测试环境的 vue-i18n 为
  // runtime-only 构建，t() 原样返回 key，故按 key 断言单位分支。
  it("renders plural admin-form validity units instead of mislabeled days (#4607)", () => {
    expect(mountPlanCard({ validity_days: 1, validity_unit: "months" }).text()).toContain("/ payment.perMonth");
    expect(mountPlanCard({ validity_days: 3, validity_unit: "months" }).text()).toContain("/ 3payment.months");
    expect(mountPlanCard({ validity_days: 2, validity_unit: "weeks" }).text()).toContain("/ 2payment.weeks");
    expect(mountPlanCard({ validity_days: 30, validity_unit: "day" }).text()).toContain("/ 30payment.days");
  });

  it("uses the configured currency symbol while preserving USD for legacy plans", () => {
    const cnyPlan = mountPlanCard({ currency: "CNY", original_price: 20 }).text();

    expect(cnyPlan).toContain("¥10CNY");
    expect(cnyPlan).toContain("¥20CNY");
    expect(mountPlanCard({ currency: "USD" }).text()).toContain("$10USD");
    expect(mountPlanCard({ currency: "" }).text()).toContain("$10");
  });

  it.each([
    ["long Chinese", "企业全球加速专业订阅套餐（含高级模型与优先支持）"],
    ["long English", "Enterprise Global Acceleration Subscription with Priority Support"],
    ["unbroken token", "EnterpriseGlobalAccelerationSubscriptionWithPrioritySupport1234567890"],
  ])("keeps the full %s plan title visible", (_label, name) => {
    const wrapper = mountPlanCard({ name });
    const title = wrapper.get("h3");

    expect(title.text()).toBe(name);
    expect(title.attributes("title")).toBe(name);
    expect(title.classes()).toEqual(expect.arrayContaining([
      "min-w-0",
      "break-words",
      "[overflow-wrap:anywhere]",
    ]));
    expect(title.classes()).not.toContain("truncate");
    expect(title.classes()).not.toContain("line-clamp-2");
  });

  it("keeps title, price, description, and purchase action in separate bounded regions", () => {
    const wrapper = mountPlanCard({
      name: "Enterprise Global Acceleration Subscription with Priority Support",
      price: 123.45,
      currency: "USD",
      description: "Includes advanced models and priority support.",
    });
    const title = wrapper.get("h3");
    const price = wrapper.findAll("span").find((node) => node.text() === "123.45");
    const priceRegion = title.element.parentElement?.nextElementSibling as HTMLElement | null;

    expect(title.element.parentElement?.classList).toContain("min-w-0");
    expect(priceRegion?.classList).toContain("min-w-0");
    expect(priceRegion?.textContent).toContain("/ 30payment.days");
    expect(price?.exists()).toBe(true);
    expect(wrapper.get("p").text()).toBe("Includes advanced models and priority support.");
    expect(wrapper.get("button").text()).toBe("payment.subscribeNow");
  });

  it("keeps short plan titles compact and aligned", () => {
    const wrapper = mountPlanCard({ name: "Pro", description: "" });
    const title = wrapper.get("h3");
    const priceRegion = title.element.parentElement?.nextElementSibling as HTMLElement | null;

    expect(title.text()).toBe("Pro");
    expect(title.attributes("title")).toBe("Pro");
    expect(title.classes()).toEqual(expect.arrayContaining(["text-base", "font-semibold"]));
    expect(priceRegion?.classList).toContain("min-w-0");
    expect(priceRegion?.textContent).toContain("/ 30payment.days");
  });

  it("submits the selected catalog product with its configured limits", async () => {
    const wrapper = mountPlanCard({ id: 9, daily_limit_usd: 200, monthly_limit_usd: 1500, price: 128 });
    expect(wrapper.text()).toContain("$200");
    expect(wrapper.text()).toContain("$1500");
    await wrapper.get("button").trigger("click");
    expect(wrapper.emitted("select")?.[0]?.[0]).toEqual(expect.objectContaining({ id: 9, price: 128, daily_limit_usd: 200, monthly_limit_usd: 1500 }));
  });
});
