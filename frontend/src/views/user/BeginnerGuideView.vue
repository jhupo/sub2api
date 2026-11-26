<template>
  <AppLayout>
    <div id="guide-top" class="guide-page">
      <header class="guide-intro">
        <div class="guide-intro-heading">
          <div class="min-w-0">
            <div class="guide-brand">
              <img :src="siteLogo" alt="" class="h-6 w-6 object-contain" />
              <span>{{ t('beginnerGuide.eyebrow', { site: appStore.siteName }) }}</span>
            </div>
            <h1>{{ t('beginnerGuide.heading') }}</h1>
            <p class="guide-muted mt-3 max-w-2xl">{{ t('beginnerGuide.introduction') }}</p>
          </div>
          <fieldset class="guide-os">
            <legend>{{ t('beginnerGuide.os') }}</legend>
            <div class="guide-segmented">
              <label v-for="system in systems" :key="system" :class="{ selected: os === system }">
                <input v-model="os" type="radio" name="guide-os" :value="system" class="sr-only" />
                {{ t(`beginnerGuide.${system}`) }}
              </label>
            </div>
          </fieldset>
        </div>

        <h2 class="guide-small-heading mt-7">{{ t('beginnerGuide.chooseTask') }}</h2>
        <div class="guide-tasks">
          <RouterLink
            v-for="task in tasks"
            :key="task.id"
            :to="{ path: '/guide', hash: `#${task.id}` }"
            class="guide-task"
            :class="[`task-${task.id}`, { 'guide-task-primary': task.id === 'quickstart' }]"
          >
            <span class="guide-task-icon"><Icon :name="task.icon" size="lg" /></span>
            <span class="min-w-0">
              <strong>{{ t(`beginnerGuide.tasks.${task.id}.title`) }}</strong>
              <span class="guide-task-description">{{ t(`beginnerGuide.tasks.${task.id}.description`) }}</span>
            </span>
            <Icon name="arrowRight" size="sm" class="ml-auto shrink-0 opacity-60" />
          </RouterLink>
        </div>
      </header>

      <section class="guide-addresses" aria-labelledby="guide-addresses-title">
        <h2 id="guide-addresses-title" class="guide-small-heading flex items-center gap-2">
          <Icon name="link" class="text-emerald-600 dark:text-emerald-400" />
          {{ t('beginnerGuide.addresses.title') }}
        </h2>
        <p class="guide-muted mt-2">{{ t('beginnerGuide.addresses.description') }}</p>
        <dl class="guide-endpoint-list">
          <div v-for="endpoint in endpointRows" :key="endpoint.id" class="guide-endpoint">
            <div class="min-w-0">
              <dt>{{ t(`beginnerGuide.addresses.${endpoint.id}`) }}</dt>
              <dd><code>{{ endpoint.value }}</code></dd>
            </div>
            <button
              type="button"
              class="guide-icon-button"
              :title="t('common.copy')"
              :aria-label="`${t('common.copy')}: ${t(`beginnerGuide.addresses.${endpoint.id}`)}`"
              @click="copyEndpoint(endpoint.id, endpoint.value)"
            >
              <Icon :name="copied && lastEndpoint === endpoint.id ? 'check' : 'copy'" size="sm" />
            </button>
          </div>
        </dl>
        <p class="guide-muted mt-3 text-xs">{{ t('beginnerGuide.addresses.hint') }}</p>
      </section>

      <details class="guide-mobile-toc">
        <summary><Icon name="book" size="sm" />{{ t('beginnerGuide.toc') }}<Icon name="chevronDown" size="sm" class="ml-auto" /></summary>
        <nav :aria-label="t('beginnerGuide.toc')">
          <RouterLink v-for="(chapter, index) in guideChapters" :key="chapter.id" :to="{ path: '/guide', hash: `#${chapter.id}` }">
            <span class="guide-number">{{ chapterNumber(index) }}</span>{{ chapterTitle(chapter.id) }}
          </RouterLink>
        </nav>
      </details>

      <div v-if="guideLoadError" class="guide-note guide-note-warning mt-5" role="status"><Icon name="exclamationTriangle" /><p>{{ t('beginnerGuide.loadError') }} <button type="button" class="guide-link" @click="loadPublishedGuide">{{ t('beginnerGuide.retry') }}</button></p></div>
      <div class="guide-document">
        <div class="min-w-0">
          <section
            v-for="(chapter, chapterIndex) in guideChapters"
            :id="chapter.id"
            :key="chapter.id"
            class="guide-chapter"
            :aria-labelledby="`${chapter.id}-heading`"
          >
            <header class="guide-chapter-heading">
              <span class="guide-chapter-icon" :class="`chapter-${chapter.id}`"><Icon :name="chapter.icon" size="lg" /></span>
              <div class="min-w-0">
                <div class="guide-chapter-number">{{ t('beginnerGuide.chapter', { number: chapterNumber(chapterIndex) }) }}</div>
                <h2 :id="`${chapter.id}-heading`">{{ chapterTitle(chapter.id) }}</h2>
                <p class="guide-muted mt-2">{{ customChapters[chapter.id]?.description ?? t(`beginnerGuide.chapters.${chapter.id}.description`) }}</p>
              </div>
            </header>

            <div class="guide-body">
              <GuideMarkdown v-if="customChapters[chapter.id]" :markdown="customChapters[chapter.id]!.markdown" :variables="guideVariables" />
              <template v-else-if="chapter.id === 'quickstart'">
                <div class="guide-progress-row">
                  <div
                    class="guide-progress"
                    role="progressbar"
                    :aria-label="t('beginnerGuide.chapters.quickstart.title')"
                    :aria-valuenow="completedSteps.length"
                    :aria-valuemin="0"
                    :aria-valuemax="5"
                  ><div :style="{ width: `${completedSteps.length * 20}%` }" /></div>
                  <span>{{ t('beginnerGuide.progress', { done: completedSteps.length, total: 5 }) }}</span>
                </div>
                <ol class="guide-timeline">
                  <li v-for="step in 5" :key="step" :class="{ 'step-done': completedSteps.includes(step) }">
                    <div class="guide-step-marker">
                      <Icon v-if="completedSteps.includes(step)" name="check" size="sm" />
                      <span v-else>{{ step }}</span>
                    </div>
                    <div class="min-w-0">
                      <h3>{{ t(`beginnerGuide.quick.steps.${step}.title`) }}</h3>
                      <p>{{ t(`beginnerGuide.quick.steps.${step}.description`) }}</p>
                      <p v-if="step === 1" class="mt-2">{{ t('beginnerGuide.quick.loginAddress', { address: endpoints.site }) }}</p>
                      <ol v-if="step === 3" class="guide-numbered-list guide-compact-list mt-3"><li v-for="item in 4" :key="item">{{ t(`beginnerGuide.quick.keySteps.${item}`) }}</li></ol>
                      <ul v-if="step === 4" class="guide-client-list"><li v-for="item in 3" :key="item">{{ t(`beginnerGuide.quick.clients.${item}`) }}</li></ul>
                      <div class="guide-note mt-3" :class="{ 'guide-note-warning': step === 2 }"><Icon :name="step === 2 ? 'exclamationTriangle' : 'infoCircle'" size="sm" /><p>{{ t(`beginnerGuide.quick.notes.${step}`) }}</p></div>
                      <div class="guide-step-actions">
                        <RouterLink :to="stepRoutes[step - 1]!" class="guide-link">
                          {{ t(`beginnerGuide.quick.steps.${step}.action`) }}<Icon name="arrowRight" size="sm" />
                        </RouterLink>
                        <label v-if="step > 1" class="guide-checkbox">
                          <input type="checkbox" :checked="completedSteps.includes(step)" :aria-label="t('beginnerGuide.stepComplete', { step: t(`beginnerGuide.quick.steps.${step}.title`) })" @change="toggleStep(step)" />
                          {{ t('beginnerGuide.completed') }}
                        </label>
                      </div>
                    </div>
                  </li>
                </ol>
                <div class="guide-note"><Icon name="shield" /><p>{{ t('beginnerGuide.quick.security') }}</p></div>
                <h3>{{ t('beginnerGuide.quick.requestTitle') }}</h3>
                <p>{{ t('beginnerGuide.quick.requestHint') }}</p>
                <GuideCodeBlock :label="examples.shell" :code="examples.request" />
                <h3>{{ t('beginnerGuide.quick.verifyTitle') }}</h3>
                <ul class="guide-check-list"><li v-for="item in 3" :key="item"><Icon name="check" size="sm" /><span>{{ t(`beginnerGuide.quick.verify.${item}`) }}</span></li></ul>
              </template>

              <template v-else-if="chapter.id === 'codex'">
                <h3>{{ t('beginnerGuide.codex.installTitle') }}</h3>
                <p>{{ t('beginnerGuide.codex.installDescription') }}</p>
                <a href="https://nodejs.org/en/download" target="_blank" rel="noopener noreferrer" class="guide-link">{{ t('beginnerGuide.codex.nodeWebsite') }}<Icon name="externalLink" size="sm" /></a>
                <GuideCodeBlock :label="examples.shell" :code="examples.nodeCheck" />
                <GuideCodeBlock :label="examples.shell" :code="examples.codexInstall" />
                <h3>{{ t('beginnerGuide.codex.autoTitle') }}</h3>
                <p>{{ t('beginnerGuide.codex.autoDescription') }}</p>
                <RouterLink to="/keys" class="guide-action"><Icon name="key" size="sm" />{{ t('beginnerGuide.codex.openKey') }}<Icon name="arrowRight" size="sm" /></RouterLink>
                <details class="guide-disclosure">
                  <summary>{{ t('beginnerGuide.codex.manualTitle') }}<Icon name="chevronDown" size="sm" /></summary>
                  <div>
                    <p>{{ t('beginnerGuide.codex.manualDescription') }}</p>
                    <GuideCodeBlock :label="examples.codexPath" :code="examples.codexConfig" />
                    <GuideCodeBlock :label="examples.shell" :code="examples.codexEnv" />
                    <p class="guide-muted text-xs">{{ t('beginnerGuide.codex.keyHint') }}</p>
                  </div>
                </details>
                <h3>{{ t('beginnerGuide.codex.runTitle') }}</h3>
                <GuideCodeBlock :label="examples.shell" code="codex" />
                <p>{{ t('beginnerGuide.codex.runDescription') }}</p>
                <h3>{{ t('beginnerGuide.codex.checkTitle') }}</h3>
                <div class="guide-issue-list">
                  <div v-for="item in 3" :key="item"><h4>{{ t(`beginnerGuide.codex.checks.${item}.title`) }}</h4><p>{{ t(`beginnerGuide.codex.checks.${item}.description`) }}</p></div>
                </div>
                <a href="https://developers.openai.com/codex/cli/" target="_blank" rel="noopener noreferrer" class="guide-link">{{ t('beginnerGuide.codex.official') }}<Icon name="externalLink" size="sm" /></a>
              </template>

              <template v-else-if="chapter.id === 'vscode'">
                <ol class="guide-numbered-list">
                  <li v-for="step in 4" :key="step">
                    <h3>{{ t(`beginnerGuide.vscode.steps.${step}.title`) }}</h3>
                    <p>{{ t(`beginnerGuide.vscode.steps.${step}.description`) }}</p>
                  </li>
                </ol>
                <div class="guide-note"><Icon name="infoCircle" /><p>{{ t('beginnerGuide.vscode.remote') }}</p></div>
                <h3>{{ t('beginnerGuide.vscode.environmentTitle') }}</h3>
                <div class="guide-table-wrap">
                  <table>
                    <caption class="sr-only">{{ t('beginnerGuide.vscode.environmentTitle') }}</caption>
                    <thead><tr><th scope="col">{{ t('beginnerGuide.vscode.environment') }}</th><th scope="col">{{ t('beginnerGuide.vscode.location') }}</th></tr></thead>
                    <tbody><tr v-for="item in 3" :key="item"><th scope="row">{{ t(`beginnerGuide.vscode.environments.${item}.name`) }}</th><td>{{ t(`beginnerGuide.vscode.environments.${item}.description`) }}</td></tr></tbody>
                  </table>
                </div>
                <p class="guide-muted text-xs">{{ t('beginnerGuide.vscode.verifyHint') }}</p>
                <a href="https://developers.openai.com/codex/ide/" target="_blank" rel="noopener noreferrer" class="guide-link">{{ t('beginnerGuide.vscode.official') }}<Icon name="externalLink" size="sm" /></a>
              </template>

              <template v-else-if="chapter.id === 'claude'">
                <h3>{{ t('beginnerGuide.claude.installTitle') }}</h3>
                <p>{{ t('beginnerGuide.claude.installDescription') }}</p>
                <a href="https://code.claude.com/docs/en/setup" target="_blank" rel="noopener noreferrer" class="guide-link">{{ t('beginnerGuide.claude.official') }}<Icon name="externalLink" size="sm" /></a>
                <GuideCodeBlock :label="examples.shell" code="claude --version" />
                <h3>{{ t('beginnerGuide.claude.envTitle') }}</h3>
                <p>{{ t('beginnerGuide.claude.envDescription') }}</p>
                <div class="guide-table-wrap">
                  <table>
                    <caption class="sr-only">{{ t('beginnerGuide.claude.variableTitle') }}</caption>
                    <thead><tr><th scope="col">{{ t('beginnerGuide.claude.variable') }}</th><th scope="col">{{ t('beginnerGuide.claude.value') }}</th></tr></thead>
                    <tbody>
                      <tr><th scope="row"><code>ANTHROPIC_BASE_URL</code></th><td><code>{{ endpoints.anthropic }}</code></td></tr>
                      <tr><th scope="row"><code>ANTHROPIC_AUTH_TOKEN</code></th><td>{{ t('beginnerGuide.claude.tokenValue') }}</td></tr>
                    </tbody>
                  </table>
                </div>
                <GuideCodeBlock :label="examples.shell" :code="examples.claudeEnv" />
                <p class="guide-muted text-xs">{{ t('beginnerGuide.claude.sessionHint') }}</p>
                <details class="guide-disclosure">
                  <summary>{{ t('beginnerGuide.claude.settingsTitle') }}<Icon name="chevronDown" size="sm" /></summary>
                  <div>
                    <p>{{ t('beginnerGuide.claude.settingsDescription') }}</p>
                    <GuideCodeBlock :label="examples.claudePath" :code="examples.claudeSettings" />
                  </div>
                </details>
                <h3>{{ t('beginnerGuide.claude.runTitle') }}</h3>
                <GuideCodeBlock :label="examples.shell" code="claude" />
                <p>{{ t('beginnerGuide.claude.runDescription') }}</p>
                <ul class="guide-check-list"><li v-for="item in 3" :key="item"><Icon name="check" size="sm" /><span>{{ t(`beginnerGuide.claude.verify.${item}`) }}</span></li></ul>
                <div class="guide-note"><Icon name="lightbulb" /><p>{{ t('beginnerGuide.claude.loginHint') }}</p></div>
              </template>

              <template v-else-if="chapter.id === 'ccswitch'">
                <p>{{ t('beginnerGuide.ccswitch.introduction') }}</p>
                <h3 class="guide-heading-with-badge"><span class="guide-badge">{{ t('beginnerGuide.recommended') }}</span>{{ t('beginnerGuide.ccswitch.autoTitle') }}</h3>
                <ol class="guide-numbered-list guide-compact-list"><li v-for="step in 4" :key="step">{{ t(`beginnerGuide.ccswitch.steps.${step}`) }}</li></ol>
                <RouterLink to="/keys" class="guide-action"><Icon name="key" size="sm" />{{ t('beginnerGuide.codex.openKey') }}<Icon name="arrowRight" size="sm" /></RouterLink>
                <p class="guide-muted text-xs">{{ t('beginnerGuide.ccswitch.importHint') }}</p>
                <h3>{{ t('beginnerGuide.ccswitch.manualTitle') }}</h3>
                <div class="guide-table-wrap" tabindex="0">
                  <table class="guide-comparison-table">
                    <caption class="sr-only">{{ t('beginnerGuide.ccswitch.manualTitle') }}</caption>
                    <thead><tr><th scope="col">{{ t('beginnerGuide.field') }}</th><th scope="col">Codex</th><th scope="col">Claude Code</th></tr></thead>
                    <tbody>
                      <tr><th scope="row">{{ t('beginnerGuide.ccswitch.name') }}</th><td>{{ appStore.siteName }}</td><td>{{ appStore.siteName }}</td></tr>
                      <tr><th scope="row">{{ t('beginnerGuide.ccswitch.endpoint') }}</th><td><code>{{ endpoints.openai }}</code></td><td><code>{{ endpoints.anthropic }}</code></td></tr>
                      <tr><th scope="row">API Key</th><td>{{ t('beginnerGuide.ccswitch.keyValue') }}</td><td>{{ t('beginnerGuide.ccswitch.keyValue') }}</td></tr>
                      <tr><th scope="row">{{ t('beginnerGuide.model') }}</th><td>{{ t('beginnerGuide.ccswitch.codexModel') }}</td><td>{{ t('beginnerGuide.ccswitch.claudeModel') }}</td></tr>
                      <tr><th scope="row">{{ t('beginnerGuide.ccswitch.protocol') }}</th><td>Responses</td><td>Anthropic Messages</td></tr>
                    </tbody>
                  </table>
                </div>
                <div class="guide-note guide-note-warning"><Icon name="exclamationTriangle" /><p>{{ t('beginnerGuide.ccswitch.pathWarning') }}</p></div>
                <h3>{{ t('beginnerGuide.ccswitch.verifyTitle') }}</h3>
                <p>{{ t('beginnerGuide.ccswitch.verifyDescription') }}</p>
                <RouterLink to="/usage" class="guide-link">{{ t('beginnerGuide.viewUsage') }}<Icon name="arrowRight" size="sm" /></RouterLink>
              </template>

              <template v-else-if="chapter.id === 'chat'">
                <p>{{ t('beginnerGuide.chat.introduction') }}</p>
                <h3>{{ t('beginnerGuide.chat.protocolTitle') }}</h3>
                <div class="guide-table-wrap" tabindex="0">
                  <table class="guide-comparison-table">
                    <caption class="sr-only">{{ t('beginnerGuide.chat.protocolTitle') }}</caption>
                    <thead><tr><th scope="col">{{ t('beginnerGuide.ccswitch.protocol') }}</th><th scope="col">Base URL</th><th scope="col">{{ t('beginnerGuide.chat.requestPath') }}</th></tr></thead>
                    <tbody>
                      <tr><th scope="row">Chat Completions</th><td><code>{{ endpoints.openai }}</code></td><td><code>/v1/chat/completions</code></td></tr>
                      <tr><th scope="row">Responses</th><td><code>{{ endpoints.openai }}</code></td><td><code>/v1/responses</code></td></tr>
                      <tr><th scope="row">Anthropic Messages</th><td><code>{{ endpoints.anthropic }}</code></td><td><code>/v1/messages</code></td></tr>
                    </tbody>
                  </table>
                </div>
                <p class="guide-muted text-xs">{{ t('beginnerGuide.chat.protocolHint') }}</p>
                <h3>Cherry Studio</h3>
                <ol class="guide-numbered-list guide-compact-list"><li v-for="step in 5" :key="step">{{ t(`beginnerGuide.chat.cherrySteps.${step}`, { site: appStore.siteName }) }}</li></ol>
                <p>{{ t('beginnerGuide.chat.pathDescription') }}</p>
                <GuideCodeBlock label="Chat Completions" :code="`${endpoints.openai}/chat/completions`" />
                <div class="guide-note guide-note-warning"><Icon name="exclamationTriangle" /><p>{{ t('beginnerGuide.chat.pathWarning') }}</p></div>
                <h3>Chatbox</h3>
                <ol class="guide-numbered-list guide-compact-list"><li v-for="step in 3" :key="step">{{ t(`beginnerGuide.chat.chatboxSteps.${step}`) }}</li></ol>
                <div class="guide-table-wrap">
                  <table>
                    <caption class="sr-only">{{ t('beginnerGuide.chat.chatboxTable') }}</caption>
                    <thead><tr><th scope="col">{{ t('beginnerGuide.field') }}</th><th scope="col">{{ t('beginnerGuide.chat.chatboxTable') }}</th></tr></thead>
                    <tbody>
                      <tr><th scope="row">{{ t('beginnerGuide.chat.apiRoot') }}</th><td><code>{{ endpoints.anthropic }}</code></td></tr>
                      <tr><th scope="row">{{ t('beginnerGuide.chat.apiPath') }}</th><td><code>/v1/chat/completions</code></td></tr>
                      <tr><th scope="row">API Key</th><td>{{ t('beginnerGuide.ccswitch.keyValue') }}</td></tr>
                      <tr><th scope="row">{{ t('beginnerGuide.model') }}</th><td>{{ t('beginnerGuide.ccswitch.codexModel') }}</td></tr>
                    </tbody>
                  </table>
                </div>
                <p class="guide-muted text-xs">{{ t('beginnerGuide.chat.singleUrlHint', { address: endpoints.openai }) }}</p>
                <h3>{{ t('beginnerGuide.chat.modelTitle') }}</h3>
                <p>{{ t('beginnerGuide.chat.modelDescription') }}</p>
                <div class="guide-note"><Icon name="shield" /><p>{{ t('beginnerGuide.chat.modelHint') }}</p></div>
                <div class="guide-links">
                  <a href="https://www.cherry-ai.com/" target="_blank" rel="noopener noreferrer" class="guide-link">{{ t('beginnerGuide.chat.cherry') }}<Icon name="externalLink" size="sm" /></a>
                  <a href="https://chatboxai.app/" target="_blank" rel="noopener noreferrer" class="guide-link">{{ t('beginnerGuide.chat.chatbox') }}<Icon name="externalLink" size="sm" /></a>
                </div>
              </template>

              <template v-else-if="chapter.id === 'models'">
                <p>{{ t('beginnerGuide.models.introduction') }}</p>
                <h3>{{ t('beginnerGuide.models.selectionTitle') }}</h3>
                <div class="guide-table-wrap">
                  <table>
                    <caption class="sr-only">{{ t('beginnerGuide.models.selectionTitle') }}</caption>
                    <thead><tr><th scope="col">{{ t('beginnerGuide.models.useCase') }}</th><th scope="col">{{ t('beginnerGuide.models.requirement') }}</th></tr></thead>
                    <tbody><tr v-for="item in 4" :key="item"><th scope="row">{{ t(`beginnerGuide.models.useCases.${item}.name`) }}</th><td>{{ t(`beginnerGuide.models.useCases.${item}.description`) }}</td></tr></tbody>
                  </table>
                </div>
                <p class="guide-muted text-xs">{{ t('beginnerGuide.models.selectionHint') }}</p>
                <div v-for="item in 3" :key="item" class="guide-explanation">
                  <h3>{{ t(`beginnerGuide.models.items.${item}.title`) }}</h3>
                  <p>{{ t(`beginnerGuide.models.items.${item}.description`) }}</p>
                </div>
                <h3>{{ t('beginnerGuide.models.cacheTitle') }}</h3>
                <p>{{ t('beginnerGuide.models.cacheDescription') }}</p>
                <h3>{{ t('beginnerGuide.models.compareTitle') }}</h3>
                <ol class="guide-numbered-list guide-compact-list"><li v-for="item in 3" :key="item">{{ t(`beginnerGuide.models.compareSteps.${item}`) }}</li></ol>
                <RouterLink v-if="channelsEnabled" to="/available-channels" class="guide-link">{{ t('beginnerGuide.models.channels') }}<Icon name="arrowRight" size="sm" /></RouterLink>
                <RouterLink to="/usage" class="guide-link">{{ t('beginnerGuide.viewUsage') }}<Icon name="arrowRight" size="sm" /></RouterLink>
                <h3>{{ t('beginnerGuide.models.modelListTitle') }}</h3>
                <p>{{ t('beginnerGuide.models.modelListHint') }}</p>
                <GuideCodeBlock :label="examples.shell" :code="examples.models" />
              </template>

              <template v-else-if="chapter.id === 'billing'">
                <p>{{ t('beginnerGuide.billing.introduction') }}</p>
                <div class="guide-flow-grid">
                  <article class="guide-flow-item">
                    <h3><span class="guide-flow-number">1</span>{{ t('beginnerGuide.billing.buyTitle') }}</h3>
                    <ol class="guide-numbered-list guide-compact-list"><li v-for="item in 3" :key="item">{{ t(`beginnerGuide.billing.buySteps.${item}`) }}</li></ol>
                    <RouterLink v-if="paymentEnabled && !authStore.isSimpleMode" to="/purchase" class="guide-action"><Icon name="creditCard" size="sm" />{{ t('beginnerGuide.billing.purchase') }}<Icon name="arrowRight" size="sm" /></RouterLink>
                    <p v-else class="guide-muted text-xs">{{ t('beginnerGuide.billing.paymentDisabled') }}</p>
                  </article>
                  <article class="guide-flow-item">
                    <h3><span class="guide-flow-number">2</span>{{ t('beginnerGuide.billing.redeemTitle') }}</h3>
                    <ol class="guide-numbered-list guide-compact-list"><li v-for="item in 3" :key="item">{{ t(`beginnerGuide.billing.redeemSteps.${item}`) }}</li></ol>
                    <RouterLink v-if="!authStore.isSimpleMode" to="/redeem" class="guide-action"><Icon name="gift" size="sm" />{{ t('beginnerGuide.billing.redeem') }}<Icon name="arrowRight" size="sm" /></RouterLink>
                  </article>
                </div>
                <h3>{{ t('beginnerGuide.billing.walletTitle') }}</h3>
                <p>{{ t('beginnerGuide.billing.walletDescription') }}</p>
                <h3>{{ t('beginnerGuide.billing.subscriptionTitle') }}</h3>
                <div class="guide-table-wrap" tabindex="0">
                  <table class="guide-comparison-table">
                    <caption class="sr-only">{{ t('beginnerGuide.billing.subscriptionTitle') }}</caption>
                    <thead><tr><th scope="col">{{ t('beginnerGuide.billing.cardType') }}</th><th scope="col">{{ t('beginnerGuide.billing.validity') }}</th><th scope="col">{{ t('beginnerGuide.billing.quotaRule') }}</th></tr></thead>
                    <tbody><tr v-for="item in 3" :key="item"><th scope="row">{{ t(`beginnerGuide.billing.cards.${item}.name`) }}</th><td>{{ t(`beginnerGuide.billing.cards.${item}.validity`) }}</td><td>{{ t(`beginnerGuide.billing.cards.${item}.quota`) }}</td></tr></tbody>
                  </table>
                </div>
                <RouterLink v-if="!authStore.isSimpleMode" to="/subscriptions" class="guide-link">{{ t('beginnerGuide.billing.subscriptions') }}<Icon name="arrowRight" size="sm" /></RouterLink>
                <h3>{{ t('beginnerGuide.billing.priorityTitle') }}</h3>
                <p>{{ t('beginnerGuide.billing.priorityDescription') }}</p>
                <div class="guide-note"><Icon name="infoCircle" /><p>{{ t('beginnerGuide.billing.priorityExample') }}</p></div>
                <RouterLink to="/keys" class="guide-link">{{ t('beginnerGuide.codex.openKey') }}<Icon name="arrowRight" size="sm" /></RouterLink>
                <h3>{{ t('beginnerGuide.billing.limitsTitle') }}</h3>
                <p>{{ t('beginnerGuide.billing.limitsDescription') }}</p>
                <h3>{{ t('beginnerGuide.billing.issuesTitle') }}</h3>
                <div class="guide-issue-list"><div v-for="item in 3" :key="item"><h4>{{ t(`beginnerGuide.billing.issues.${item}.title`) }}</h4><p>{{ t(`beginnerGuide.billing.issues.${item}.description`) }}</p></div></div>
                <h3>{{ t('beginnerGuide.billing.usageTitle') }}</h3>
                <p>{{ t('beginnerGuide.billing.usageDescription') }}</p>
                <RouterLink to="/usage" class="guide-link">{{ t('beginnerGuide.viewUsage') }}<Icon name="arrowRight" size="sm" /></RouterLink>
                <div class="guide-note"><Icon name="clock" /><p>{{ t('beginnerGuide.billing.expiryHint') }}</p></div>
                <h3>{{ t('beginnerGuide.billing.cancelTitle') }}</h3>
                <p>{{ t('beginnerGuide.billing.cancelDescription') }}</p>
              </template>

              <template v-else-if="chapter.id === 'troubleshooting'">
                <p class="guide-error-intro">{{ t('beginnerGuide.errors.introduction') }}</p>
                <div class="guide-search">
                  <Icon name="search" class="shrink-0 text-gray-400" />
                  <input v-model="errorQuery" type="search" :placeholder="t('beginnerGuide.errors.search')" :aria-label="t('beginnerGuide.errors.search')" />
                </div>
                <p v-if="errorQuery.trim()" class="guide-muted text-xs" role="status">{{ t('beginnerGuide.errors.results', { count: filteredErrors.length }) }}</p>
                <div v-if="filteredErrors.length" class="guide-error-grid">
                  <article v-for="error in filteredErrors" :key="error.id" class="guide-error-card" :aria-labelledby="`guide-error-${error.id}`">
                    <h3 :id="`guide-error-${error.id}`">{{ error.title }}</h3>
                    <dl>
                      <div><dt>{{ t('beginnerGuide.errors.checkLabel') }}</dt><dd>{{ error.check }}</dd></div>
                      <div><dt>{{ t('beginnerGuide.errors.actionLabel') }}</dt><dd>{{ error.action }}</dd></div>
                    </dl>
                    <RouterLink v-if="error.id === 'balance' && paymentEnabled && !authStore.isSimpleMode" to="/purchase" class="guide-link">{{ t('beginnerGuide.errors.purchase') }}<Icon name="arrowRight" size="sm" /></RouterLink>
                    <RouterLink v-if="error.id === 'quota'" to="/keys" class="guide-link">{{ t('beginnerGuide.errors.checkKey') }}<Icon name="arrowRight" size="sm" /></RouterLink>
                  </article>
                </div>
                <p v-else class="guide-empty" role="status">{{ t('beginnerGuide.errors.noResults') }}</p>

                <h3>{{ t('beginnerGuide.errors.selfCheckTitle') }}</h3>
                <ul class="guide-self-check">
                  <li v-for="item in selfCheckItems" :key="item">
                    <label class="guide-checkbox">
                      <input v-model="checkedDiagnostics" type="checkbox" :value="item" />
                      <i18n-t v-if="item === 'site' || item === 'api'" :keypath="`beginnerGuide.errors.selfCheck.${item}`" tag="span">
                        <template #address><code>{{ item === 'site' ? endpoints.site : endpoints.anthropic }}</code></template>
                      </i18n-t>
                      <span v-else>{{ t(`beginnerGuide.errors.selfCheck.${item}`) }}</span>
                    </label>
                  </li>
                </ul>
                <p>{{ t('beginnerGuide.errors.connectionHint') }}</p>
                <GuideCodeBlock :label="examples.shell" :code="examples.models" />

                <h3>{{ t('beginnerGuide.errors.supportTitle') }}</h3>
                <p>{{ t('beginnerGuide.errors.supportDescription') }}</p>
                <GuideCodeBlock :label="t('beginnerGuide.errors.templateLabel')" :code="supportTemplate" />
                <div class="guide-note"><Icon name="shield" /><p>{{ t('beginnerGuide.errors.secretWarning') }}</p></div>
                <div class="guide-support">
                  <strong>{{ t('beginnerGuide.errors.contact') }}</strong>
                  <a v-if="contactUrl" :href="contactUrl" target="_blank" rel="noopener noreferrer" class="guide-link break-all">{{ appStore.contactInfo }}<Icon name="externalLink" size="sm" /></a>
                  <p v-else class="whitespace-pre-wrap break-words">{{ appStore.contactInfo || t('beginnerGuide.errors.noContact') }}</p>
                  <a v-if="documentationUrl" :href="documentationUrl" target="_blank" rel="noopener noreferrer" class="guide-link">{{ t('beginnerGuide.errors.docs') }}<Icon name="externalLink" size="sm" /></a>
                </div>
              </template>
            </div>

            <footer class="guide-chapter-footer">
              <label class="guide-checkbox">
                <input type="checkbox" :checked="completedChapters.includes(chapter.id)" @change="toggleChapter(chapter.id)" />
                {{ t(completedChapters.includes(chapter.id) ? 'beginnerGuide.read' : 'beginnerGuide.markRead') }}
              </label>
              <RouterLink v-if="guideChapters[chapterIndex + 1]" :to="{ path: '/guide', hash: `#${guideChapters[chapterIndex + 1]!.id}` }" class="guide-next">
                <span class="guide-muted text-xs">{{ t('beginnerGuide.next') }}</span>
                <span><span class="guide-number">{{ chapterNumber(chapterIndex + 1) }}</span>{{ chapterTitle(guideChapters[chapterIndex + 1]!.id) }}<Icon name="arrowRight" size="sm" /></span>
              </RouterLink>
              <a v-else href="#guide-top" class="guide-link">{{ t('beginnerGuide.backTop') }}<Icon name="arrowUp" size="sm" /></a>
            </footer>
          </section>
        </div>

        <aside class="guide-toc">
          <nav :aria-label="t('beginnerGuide.toc')">
            <div class="flex items-center justify-between gap-2">
              <h2>{{ t('beginnerGuide.toc') }}</h2>
              <button type="button" class="guide-icon-button" :title="t('beginnerGuide.reset')" :aria-label="t('beginnerGuide.reset')" @click="resetProgress"><Icon name="refresh" size="sm" /></button>
            </div>
            <RouterLink
              v-for="(chapter, index) in guideChapters"
              :key="chapter.id"
              :to="{ path: '/guide', hash: `#${chapter.id}` }"
              :class="{ active: activeChapter === chapter.id }"
              :aria-current="activeChapter === chapter.id ? 'location' : undefined"
            >
              <span class="guide-number">{{ chapterNumber(index) }}</span>
              <span>{{ chapterTitle(chapter.id) }}</span>
              <Icon v-if="completedChapters.includes(chapter.id)" name="check" size="xs" class="ml-auto shrink-0 text-emerald-600" />
              <span v-else-if="chapter.id === 'quickstart'" class="ml-auto shrink-0 text-xs">{{ completedSteps.length }}/5</span>
            </RouterLink>
            <a href="#guide-top" class="guide-toc-top"><Icon name="arrowUp" size="sm" />{{ t('beginnerGuide.backTop') }}</a>
          </nav>
        </aside>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import GuideCodeBlock from '@/components/Guide/GuideCodeBlock.vue'
import GuideMarkdown from '@/components/Guide/GuideMarkdown.vue'
import { beginnerGuideAPI, type GuideContent } from '@/api/beginnerGuide'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { useClipboard } from '@/composables/useClipboard'
import { sanitizeUrl } from '@/utils/url'
import { FeatureFlags, isFeatureFlagEnabled } from '@/utils/featureFlags'
import { getGuideEndpoints, getGuideExamples, guideChapters, guideErrorIds, readGuideProgress, type GuideChapterId, type GuideOS } from '@/utils/beginnerGuide'

const { t, locale } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()
const { copied, copyToClipboard } = useClipboard()
const systems: GuideOS[] = ['unix', 'windows']
const tasks = guideChapters.filter(chapter => ['quickstart', 'codex', 'claude', 'chat', 'billing', 'troubleshooting'].includes(chapter.id))
const stepRoutes = ['/dashboard', '/subscriptions', '/keys', '/guide#codex', '/usage']
const os = ref<GuideOS>(/Win/i.test(navigator.platform) ? 'windows' : 'unix')
const completedSteps = ref<number[]>([1])
const completedChapters = ref<GuideChapterId[]>([])
const activeChapter = ref<GuideChapterId>('quickstart')
const errorQuery = ref('')
const selfCheckItems = ['site', 'api', 'key', 'provider', 'environment'] as const
const checkedDiagnostics = ref<string[]>([])
const lastEndpoint = ref('')
const publishedGuide = ref<GuideContent>({})
const guideLoadError = ref(false)
const customChapters = computed(() => publishedGuide.value[locale.value.startsWith('zh') ? 'zh' : 'en'] ?? {})
const progressKey = computed(() => `sub2api_beginner_guide_v1_${authStore.user?.id ?? 'guest'}`)
const endpoints = computed(() => getGuideEndpoints(appStore.apiBaseUrl, window.location.origin))
const examples = computed(() => getGuideExamples(endpoints.value, os.value))
const supportTemplate = computed(() => t('beginnerGuide.errors.template', { site: endpoints.value.site, api: endpoints.value.anthropic }))
const guideVariables = computed(() => ({ site_name: appStore.siteName, site_url: endpoints.value.site, api_root: endpoints.value.anthropic, openai_base_url: endpoints.value.openai, contact_info: appStore.contactInfo || t('beginnerGuide.errors.noContact'), doc_url: appStore.docUrl }))
const chapterTitle = (id: GuideChapterId) => customChapters.value[id]?.title ?? t(`beginnerGuide.chapters.${id}.title`)
const endpointRows = computed(() => [
  { id: 'site', value: endpoints.value.site },
  { id: 'openai', value: endpoints.value.openai },
  { id: 'anthropic', value: endpoints.value.anthropic },
])
const siteLogo = computed(() => sanitizeUrl(appStore.siteLogo || '', { allowRelative: true, allowDataUrl: true }) || '/logo.svg')
const contactUrl = computed(() => sanitizeUrl(appStore.contactInfo || ''))
const documentationUrl = computed(() => sanitizeUrl(appStore.docUrl || '', { allowRelative: true }))
const paymentEnabled = computed(() => isFeatureFlagEnabled(FeatureFlags.payment))
const channelsEnabled = computed(() => isFeatureFlagEnabled(FeatureFlags.availableChannels))
const filteredErrors = computed(() => {
  const normalize = (text: string) => text.toLocaleLowerCase().replace(/[_-]+/g, ' ')
  const words = normalize(errorQuery.value.trim()).split(/\s+/).filter(Boolean)
  return guideErrorIds.map(id => ({
    id,
    title: t(`beginnerGuide.errors.${id}.title`),
    check: t(`beginnerGuide.errors.${id}.check`),
    action: t(`beginnerGuide.errors.${id}.action`),
  })).filter(error => words.every(word => normalize(`${error.title} ${error.check} ${error.action}`).includes(word)))
})

const chapterNumber = (index: number) => String(index + 1).padStart(2, '0')

watch(progressKey, key => {
  const progress = readGuideProgress(localStorage, key)
  completedSteps.value = [...new Set([1, ...progress.steps])].sort()
  completedChapters.value = progress.chapters
}, { immediate: true })

function persistProgress() {
  try {
    localStorage.setItem(progressKey.value, JSON.stringify({ steps: completedSteps.value, chapters: completedChapters.value }))
  } catch {
    // The guide remains usable when browser storage is unavailable.
  }
}

function toggleStep(step: number) {
  completedSteps.value = completedSteps.value.includes(step) ? completedSteps.value.filter(value => value !== step) : [...completedSteps.value, step].sort()
  persistProgress()
}

function toggleChapter(id: GuideChapterId) {
  completedChapters.value = completedChapters.value.includes(id) ? completedChapters.value.filter(value => value !== id) : [...completedChapters.value, id]
  persistProgress()
}

function resetProgress() {
  completedSteps.value = [1]
  completedChapters.value = []
  persistProgress()
}

async function copyEndpoint(id: string, value: string) {
  if (await copyToClipboard(value)) lastEndpoint.value = id
}

let scrollFrame = 0
async function loadPublishedGuide() {
  try {
    publishedGuide.value = (await beginnerGuideAPI.published()).content
    guideLoadError.value = false
    await nextTick()
    const id = window.location.hash.slice(1)
    if (guideChapters.some(chapter => chapter.id === id)) document.getElementById(id)?.scrollIntoView()
    updateActiveChapter()
  } catch {
    guideLoadError.value = true
  }
}
function updateActiveChapter() {
  if (scrollFrame) return
  scrollFrame = requestAnimationFrame(() => {
    scrollFrame = 0
    let current: GuideChapterId = 'quickstart'
    for (const chapter of guideChapters) {
      if ((document.getElementById(chapter.id)?.getBoundingClientRect().top ?? Infinity) <= 140) current = chapter.id
    }
    activeChapter.value = current
  })
}

onMounted(() => {
  void loadPublishedGuide()
  window.addEventListener('scroll', updateActiveChapter, { passive: true })
  updateActiveChapter()
})

onUnmounted(() => {
  window.removeEventListener('scroll', updateActiveChapter)
  cancelAnimationFrame(scrollFrame)
})
</script>

<style scoped>
.guide-page { --guide-line: #e5e7eb; --guide-muted: #6b7280; --guide-surface: #f8fafb; --guide-text: #1f2937; max-width: 1280px; margin: 0 auto; padding: 28px 32px; background: #fff; color: var(--guide-text); font-size: 14px; line-height: 1.9; overflow-wrap: anywhere; letter-spacing: 0; }
.dark .guide-page { --guide-line: #334155; --guide-muted: #94a3b8; --guide-surface: #192331; --guide-text: #e2e8f0; background: #0f172a; }
.guide-page h1 { font-size: 28px; line-height: 1.4; font-weight: 700; }
.guide-muted { color: var(--guide-muted); }
.guide-brand { display: flex; align-items: center; gap: 8px; margin-bottom: 14px; color: var(--guide-muted); font-size: 12px; }
.guide-intro { padding-bottom: 28px; }
.guide-intro-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 24px; }
.guide-os { flex-shrink: 0; }
.guide-os legend { margin-bottom: 7px; color: var(--guide-muted); font-size: 12px; }
.guide-segmented { display: flex; padding: 3px; border: 1px solid var(--guide-line); border-radius: 6px; background: var(--guide-surface); }
.guide-segmented label { padding: 6px 12px; border-radius: 4px; cursor: pointer; color: var(--guide-muted); font-size: 12px; white-space: nowrap; }
.guide-segmented label.selected { background: #fff; color: #1f2937; box-shadow: 0 1px 3px #00000012; }
.guide-segmented label:focus-within { outline: 2px solid #60a5fa; }
.dark .guide-segmented label.selected { background: #334155; color: #fff; }
.guide-small-heading { font-size: 14px; font-weight: 600; }
.guide-tasks { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; margin-top: 12px; }
.guide-task { display: flex; min-height: 82px; align-items: center; gap: 12px; padding: 14px; border: 1px solid var(--guide-line); border-radius: 8px; transition: border-color .15s, background-color .15s; }
.guide-task:hover { border-color: #93c5fd; background: var(--guide-surface); }
.guide-task strong { display: block; font-size: 13px; font-weight: 600; line-height: 1.6; }
.guide-task-description { display: block; margin-top: 3px; color: var(--guide-muted); font-size: 11px; line-height: 1.6; }
.guide-task-icon { display: flex; width: 34px; height: 34px; flex-shrink: 0; align-items: center; justify-content: center; color: #2563eb; background: #eff6ff; border-radius: 6px; }
.task-claude .guide-task-icon { color: #a34c2b; background: #fff3ed; }
.task-chat .guide-task-icon { color: #0f766e; background: #f0fdfa; }
.task-billing .guide-task-icon { color: #a16207; background: #fefce8; }
.task-troubleshooting .guide-task-icon { color: #be123c; background: #fff1f2; }
.guide-task-primary, .guide-task-primary:hover { border-color: #2563eb; background: #2563eb; color: #fff; }
.guide-task-primary .guide-task-description { color: #dbeafe; }
.guide-task-primary .guide-task-icon { color: #fff; background: #ffffff20; }
.guide-addresses { padding: 26px 0 30px; border-top: 1px solid var(--guide-line); border-bottom: 1px solid var(--guide-line); }
.guide-endpoint-list { margin-top: 16px; }
.guide-endpoint { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 12px 16px; background: var(--guide-surface); border-bottom: 1px solid var(--guide-line); }
.guide-endpoint dt { color: var(--guide-muted); font-size: 11px; }
.guide-endpoint dd { margin-top: 3px; font-size: 13px; }
.guide-icon-button { display: inline-flex; width: 32px; height: 32px; flex-shrink: 0; align-items: center; justify-content: center; border-radius: 4px; color: var(--guide-muted); }
.guide-icon-button:hover { background: #9ca3af20; color: #2563eb; }
.guide-document { display: grid; grid-template-columns: minmax(0, 1fr) 212px; gap: 40px; }
.guide-chapter { padding: 40px 0 32px; scroll-margin-top: 88px; border-bottom: 1px solid var(--guide-line); }
.guide-chapter:last-child { border: 0; }
.guide-chapter-heading { display: flex; gap: 14px; align-items: flex-start; margin-bottom: 26px; }
.guide-chapter-icon { display: flex; width: 42px; height: 42px; flex-shrink: 0; align-items: center; justify-content: center; border-radius: 8px; background: #eff6ff; color: #2563eb; }
.chapter-quickstart, .chapter-models { background: #ecfdf5; color: #047857; }
.chapter-claude { background: #fff3ed; color: #a34c2b; }
.chapter-billing { background: #fefce8; color: #a16207; }
.chapter-troubleshooting { background: #fff1f2; color: #be123c; }
.guide-chapter-number { color: var(--guide-muted); font-size: 11px; }
.guide-chapter h2 { margin-top: 2px; font-size: 21px; font-weight: 650; line-height: 1.5; }
.guide-body { display: flex; flex-direction: column; align-items: stretch; gap: 16px; }
.guide-body h3 { margin-top: 16px; font-size: 16px; font-weight: 600; line-height: 1.7; }
.guide-body > h3:first-child { margin-top: 0; }
.guide-body p { color: var(--guide-text); }
.guide-body .guide-muted { color: var(--guide-muted); }
.guide-link { display: inline-flex; align-items: center; align-self: flex-start; gap: 6px; color: #2563eb; font-size: 13px; }
.guide-link:hover { text-decoration: underline; text-underline-offset: 3px; }
.dark .guide-link { color: #93c5fd; }
.guide-link svg { flex-shrink: 0; }
.guide-action { display: inline-flex; align-self: flex-start; align-items: center; justify-content: center; gap: 8px; padding: 8px 14px; border: 1px solid #bfdbfe; border-radius: 6px; background: #eff6ff; color: #1d4ed8; font-size: 13px; font-weight: 500; }
.guide-action:hover { background: #dbeafe; }
.dark .guide-action { border-color: #1e40af; background: #172554; color: #bfdbfe; }
.dark .guide-action:hover { background: #1e3a8a; }
.guide-links { display: flex; flex-wrap: wrap; gap: 12px 20px; align-items: center; }
.guide-progress-row { display: flex; align-items: center; gap: 14px; margin: 0 0 8px; }
.guide-progress-row > span { flex-shrink: 0; color: var(--guide-muted); font-size: 11px; }
.guide-progress { width: 100%; height: 6px; overflow: hidden; background: var(--guide-line); border-radius: 3px; }
.guide-progress > div { height: 100%; background: #059669; border-radius: inherit; transition: width .2s; }
.guide-timeline { padding: 0; list-style: none; }
.guide-timeline > li { position: relative; display: grid; grid-template-columns: 28px minmax(0, 1fr); gap: 14px; padding-bottom: 24px; }
.guide-timeline > li:last-child { padding-bottom: 0; }
.guide-timeline > li:not(:last-child)::before { content: ''; position: absolute; left: 13px; top: 28px; bottom: 0; width: 1px; background: var(--guide-line); }
.guide-step-marker { position: relative; display: flex; width: 28px; height: 28px; align-items: center; justify-content: center; border: 1px solid var(--guide-line); border-radius: 50%; color: var(--guide-muted); font-size: 12px; }
.step-done .guide-step-marker { border-color: #059669; background: #059669; color: #fff; }
.guide-timeline h3 { margin: 1px 0 6px; font-size: 14px; }
.guide-client-list { margin-top: 12px; padding-left: 20px; list-style: disc; }
.guide-client-list li { margin: 6px 0; }
.guide-step-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 20px; margin-top: 10px; }
.guide-checkbox { display: inline-flex; align-items: center; gap: 8px; cursor: pointer; color: var(--guide-muted); font-size: 12px; }
.guide-checkbox input { width: 15px; height: 15px; flex-shrink: 0; accent-color: #059669; }
.guide-note { display: flex; align-items: flex-start; gap: 10px; padding: 12px 14px; border-left: 2px solid #34d399; background: var(--guide-surface); font-size: 12px; }
.guide-note > svg { flex-shrink: 0; margin-top: 2px; color: #059669; }
.guide-note-warning { border-left-color: #f59e0b; background: #fffbeb; }
.guide-note-warning > svg { color: #b45309; }
.guide-note-warning p { color: #92400e; }
.dark .guide-note-warning { background: #382711; }
.dark .guide-note-warning p, .dark .guide-note-warning > svg { color: #fcd34d; }
.guide-heading-with-badge { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.guide-badge { flex-shrink: 0; padding: 2px 7px; border-radius: 4px; background: #ecfdf5; color: #047857; font-size: 11px; font-weight: 600; }
.dark .guide-badge { background: #063d31; color: #6ee7b7; }
.guide-check-list { display: grid; gap: 10px; }
.guide-check-list li { display: flex; align-items: flex-start; gap: 10px; }
.guide-check-list svg { flex-shrink: 0; margin-top: 5px; color: #059669; }
.guide-issue-list { border-top: 1px solid var(--guide-line); border-bottom: 1px solid var(--guide-line); }
.guide-issue-list > div { padding: 14px 0; }
.guide-issue-list > div + div { border-top: 1px solid var(--guide-line); }
.guide-issue-list h4 { margin-bottom: 5px; font-size: 14px; font-weight: 600; }
.guide-issue-list p { font-size: 13px; }
.guide-flow-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; margin: 4px 0; }
.guide-flow-item { display: flex; min-width: 0; flex-direction: column; align-items: flex-start; gap: 14px; padding: 18px; border: 1px solid var(--guide-line); border-radius: 8px; }
.guide-flow-item h3 { display: flex; align-items: center; gap: 8px; margin: 0; font-size: 14px; }
.guide-flow-number { display: inline-flex; width: 22px; height: 22px; flex-shrink: 0; align-items: center; justify-content: center; border-radius: 50%; background: #047857; color: #fff; font-size: 12px; }
.guide-flow-item ol { font-size: 13px; }
.guide-flow-item .guide-action { margin-top: auto; }
.guide-disclosure { margin: 6px 0; border-top: 1px solid var(--guide-line); border-bottom: 1px solid var(--guide-line); }
.guide-disclosure summary { display: flex; align-items: center; justify-content: space-between; gap: 12px; cursor: pointer; list-style: none; padding: 14px 0; font-size: 13px; font-weight: 500; }
summary::-webkit-details-marker { display: none; }
summary > svg { flex-shrink: 0; transition: transform .15s; }
details[open] > summary > svg:last-child { transform: rotate(180deg); }
.guide-disclosure > div { display: flex; flex-direction: column; gap: 14px; padding: 0 0 18px; }
.guide-numbered-list { list-style: decimal; padding-left: 22px; }
.guide-numbered-list li { padding-left: 4px; margin-bottom: 20px; }
.guide-numbered-list li::marker { color: #059669; font-size: 13px; font-weight: 600; }
.guide-numbered-list h3 { margin: 0 0 6px; }
.guide-numbered-list li:last-child { margin-bottom: 0; }
.guide-compact-list li { margin-bottom: 8px; }
.guide-table-wrap { min-width: 0; max-width: 100%; overflow-x: auto; border: 1px solid var(--guide-line); border-radius: 6px; }
.guide-page table { width: 100%; border-collapse: collapse; text-align: left; font-size: 13px; line-height: 1.8; }
.guide-page table.guide-comparison-table { min-width: 520px; }
.guide-page th, .guide-page td { padding: 10px 12px; border-bottom: 1px solid var(--guide-line); vertical-align: top; }
.guide-page tbody tr:last-child > * { border-bottom: 0; }
.guide-page thead { color: var(--guide-muted); background: var(--guide-surface); }
.guide-page th { font-weight: 500; }
.guide-page tbody th { width: 26%; }
.guide-page td code, .guide-page th code { padding: 2px 4px; border-radius: 3px; background: var(--guide-surface); font-size: 12px; overflow-wrap: anywhere; }
.guide-explanation { padding-bottom: 14px; border-bottom: 1px solid var(--guide-line); }
.guide-explanation h3 { margin: 0 0 6px; }
.guide-chapter-footer { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 16px; margin-top: 28px; padding-top: 18px; border-top: 1px solid var(--guide-line); }
.guide-next { margin-left: auto; text-align: right; font-size: 12px; }
.guide-next > span { display: flex; align-items: center; justify-content: flex-end; gap: 8px; }
.guide-next:hover { color: #2563eb; }
.guide-toc { padding-top: 30px; }
.guide-toc nav { position: sticky; top: 88px; max-height: calc(100dvh - 110px); overflow-y: auto; }
.guide-toc h2 { color: var(--guide-muted); font-size: 12px; font-weight: 500; }
.guide-toc a, .guide-mobile-toc nav a { display: flex; gap: 10px; align-items: center; padding: 9px 10px; border-left: 2px solid var(--guide-line); color: var(--guide-muted); font-size: 12px; line-height: 1.6; }
.guide-toc a.active { border-color: #059669; background: var(--guide-surface); color: #047857; font-weight: 600; }
.dark .guide-toc a.active { color: #6ee7b7; }
.guide-toc a:hover, .guide-mobile-toc nav a:hover { background: var(--guide-surface); color: var(--guide-text); }
.guide-number { flex-shrink: 0; font-family: ui-monospace, monospace; font-size: 11px; opacity: .7; }
.guide-toc a.guide-toc-top { margin-top: 18px; border-left-color: transparent; }
.guide-mobile-toc { display: none; }
.guide-search { display: flex; align-items: center; gap: 10px; padding: 10px 14px; border: 1px solid var(--guide-line); border-radius: 6px; background: var(--guide-surface); }
.guide-search:focus-within { border-color: #60a5fa; outline: 2px solid #60a5fa30; }
.guide-search input { width: 100%; min-width: 0; border: 0; background: transparent; outline: 0; color: var(--guide-text); font-size: 13px; }
.guide-body .guide-error-intro { color: var(--guide-text); font-size: 13px; }
.guide-error-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.guide-error-card { min-width: 0; padding: 16px; border: 1px solid var(--guide-line); border-radius: 8px; }
.guide-error-card h3 { margin: 0 0 12px; font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: 13px; font-weight: 600; line-height: 1.7; }
.guide-error-card dl { display: grid; gap: 10px; }
.guide-error-card dt { color: var(--guide-muted); font-size: 11px; line-height: 1.6; }
.guide-error-card dd { margin-top: 2px; font-size: 13px; line-height: 1.8; }
.guide-error-card .guide-link { margin-top: 12px; }
.guide-self-check { display: grid; gap: 10px; }
.guide-self-check .guide-checkbox { align-items: flex-start; color: var(--guide-text); font-size: 13px; }
.guide-self-check input { margin-top: 4px; }
.guide-self-check code { padding: 2px 5px; border-radius: 3px; background: var(--guide-surface); font-size: 12px; overflow-wrap: anywhere; }
.guide-self-check label > span { min-width: 0; }
.guide-empty { padding: 24px 0; text-align: center; font-size: 13px; }
.guide-support { display: flex; flex-direction: column; gap: 8px; padding-top: 6px; font-size: 13px; }
.guide-page :is(a, button, summary, input, .guide-table-wrap):focus-visible { outline: 2px solid #60a5fa; outline-offset: 3px; }
@media (max-width: 1279px) {
  .guide-document { display: block; }
  .guide-toc { display: none; }
  .guide-mobile-toc { display: block; margin-top: 20px; padding: 12px 0; border-bottom: 1px solid var(--guide-line); }
  .guide-mobile-toc summary { display: flex; align-items: center; gap: 8px; cursor: pointer; list-style: none; font-size: 13px; }
  .guide-mobile-toc nav { display: grid; grid-template-columns: 1fr 1fr; gap: 0 12px; margin: 12px 0 4px; }
  .guide-tasks { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .guide-intro-heading { flex-wrap: wrap; }
}
@media (max-width: 639px) {
  .guide-error-grid { grid-template-columns: minmax(0, 1fr); }
  .guide-flow-grid { grid-template-columns: minmax(0, 1fr); }
  .guide-flow-item { padding: 16px; }
  .guide-page table { font-size: 12px; }
  .guide-page th, .guide-page td { padding: 10px; }
  .guide-page { padding: 22px 18px; font-size: 13px; }
  .guide-page h1 { font-size: 24px; }
  .guide-intro-heading { gap: 18px; }
  .guide-task { min-height: 106px; align-items: flex-start; gap: 8px; padding: 12px; flex-wrap: wrap; }
  .guide-task > span:nth-child(2) { width: 100%; }
  .guide-task > svg { display: none; }
  .guide-task-icon { height: 28px; width: 28px; }
  .guide-task-icon svg { width: 18px; height: 18px; }
  .guide-task-description { font-size: 10px; }
  .guide-endpoint { padding: 12px; }
  .guide-endpoint dd { font-size: 11px; }
  .guide-chapter { padding-top: 28px; }
  .guide-chapter h2 { font-size: 19px; }
  .guide-chapter-heading { gap: 12px; }
  .guide-chapter-icon { width: 36px; height: 36px; }
  .guide-timeline > li { gap: 10px; }
  .guide-mobile-toc nav { grid-template-columns: 1fr; }
}
@media (prefers-reduced-motion: reduce) {
  .guide-page *, .guide-page *::before, .guide-page *::after { transition: none; }
}
</style>
