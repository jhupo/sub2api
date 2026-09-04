import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import { createPinia } from "pinia";
import { createI18n } from "vue-i18n";
import type { SubscriptionPlan } from "@/types/payment";
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
  ])("keeps the full %s plan title accessible in a bounded two-line area", (_label, name) => {
    const wrapper = mountPlanCard({ name });
    const title = wrapper.get("h3");

    expect(title.text()).toBe(name);
    expect(title.attributes("title")).toBe(name);
    expect(title.classes()).toEqual(expect.arrayContaining([
      "min-w-0",
      "h-12",
      "break-words",
      "line-clamp-2",
      "[overflow-wrap:anywhere]",
    ]));
    expect(title.classes()).not.toContain("truncate");
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
    expect(title.element.parentElement?.classList).toContain("flex-1");
    expect([...(priceRegion?.classList ?? [])]).toEqual(expect.arrayContaining(["shrink-0", "text-right"]));
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
    expect(title.classes()).toEqual(expect.arrayContaining(["text-base", "font-bold", "h-12"]));
    expect([...(priceRegion?.classList ?? [])]).toEqual(expect.arrayContaining(["shrink-0", "text-right"]));
    expect(priceRegion?.textContent).toContain("/ 30payment.days");
  });
});
