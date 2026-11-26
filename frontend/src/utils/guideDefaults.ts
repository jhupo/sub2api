import { getGuideExamples, type GuideChapterId, type GuideOS } from './beginnerGuide'
import type { GuideChapterContent } from '@/api/beginnerGuide'

type Translate = (key: string, params?: Record<string, string | number>) => string

export function getDefaultGuideChapter(id: GuideChapterId, translate: Translate, os: GuideOS): GuideChapterContent {
  const t = (key: string, params?: Record<string, string | number>) => translate(`beginnerGuide.${key}`, params)
  const examples = getGuideExamples({ site: '{{site_url}}', anthropic: '{{api_root}}', openai: '{{openai_base_url}}' }, os)
  const heading = (key: string) => `## ${t(key)}`
  const code = (value: string, language = 'text') => `\`\`\`${language}\n${value}\n\`\`\``
  const note = (key: string) => `> ${t(key)}`
  const link = (label: string, url: string) => `[${t(label)}](${url})`
  const list = (prefix: string, count: number, ordered = true) => Array.from({ length: count }, (_, index) => `${ordered ? `${index + 1}.` : '-'} ${t(`${prefix}.${index + 1}`)}`).join('\n')
  const table = (rows: string[][]) => {
    const cells = rows.map(row => `| ${row.map(cell => cell.replace(/\|/g, '\\|').replace(/\n/g, ' ')).join(' | ')} |`)
    cells.splice(1, 0, `| ${rows[0]!.map(() => '---').join(' | ')} |`)
    return cells.join('\n')
  }
  const body: string[] = []
  switch (id) {
    case 'quickstart': {
      const routes = ['/dashboard', '/subscriptions', '/keys', '/keys', '/usage']
      for (let step = 1; step <= 5; step++) {
        body.push(`## ${step}. ${t(`quick.steps.${step}.title`)}`, t(`quick.steps.${step}.description`))
        if (step === 1) body.push(t('quick.loginAddress', { address: '{{site_url}}' }))
        if (step === 3) body.push(list('quick.keySteps', 4))
        if (step === 4) body.push(list('quick.clients', 3, false))
        body.push(note(`quick.notes.${step}`), link(`quick.steps.${step}.action`, routes[step - 1]!))
      }
      body.push(heading('quick.requestTitle'), t('quick.requestHint'), code(examples.request, examples.shell), heading('quick.verifyTitle'), list('quick.verify', 3, false), note('quick.security'))
      break
    }
    case 'codex':
      body.push(heading('codex.installTitle'), t('codex.installDescription'), `[${t('codex.nodeWebsite')}](https://nodejs.org/en/download)`, code(examples.nodeCheck, examples.shell), code(examples.codexInstall, examples.shell), heading('codex.autoTitle'), t('codex.autoDescription'), link('codex.openKey', '/keys'), heading('codex.manualTitle'), t('codex.manualDescription'), code(examples.codexConfig, 'toml'), `**${examples.codexPath}**`, code(examples.codexEnv, examples.shell), note('codex.keyHint'), heading('codex.runTitle'), code('codex', examples.shell), t('codex.runDescription'), heading('codex.checkTitle'))
      for (let n = 1; n <= 3; n++) body.push(`### ${t(`codex.checks.${n}.title`)}`, t(`codex.checks.${n}.description`))
      body.push(`[${t('codex.official')}](https://developers.openai.com/codex/cli/)`)
      break
    case 'vscode':
      for (let n = 1; n <= 4; n++) body.push(`## ${n}. ${t(`vscode.steps.${n}.title`)}`, t(`vscode.steps.${n}.description`))
      body.push(note('vscode.remote'), heading('vscode.environmentTitle'), table([[t('vscode.environment'), t('vscode.location')], ...[1, 2, 3].map(n => [t(`vscode.environments.${n}.name`), t(`vscode.environments.${n}.description`)])]), t('vscode.verifyHint'), `[${t('vscode.official')}](https://developers.openai.com/codex/ide/)`)
      break
    case 'claude':
      body.push(heading('claude.installTitle'), t('claude.installDescription'), `[${t('claude.official')}](https://code.claude.com/docs/en/setup)`, code('claude --version', examples.shell), heading('claude.envTitle'), t('claude.envDescription'), table([[t('claude.variable'), t('claude.value')], ['`ANTHROPIC_BASE_URL`', '`{{api_root}}`'], ['`ANTHROPIC_AUTH_TOKEN`', t('claude.tokenValue')]]), code(examples.claudeEnv, examples.shell), note('claude.sessionHint'), heading('claude.settingsTitle'), t('claude.settingsDescription'), `**${examples.claudePath}**`, code(examples.claudeSettings, 'json'), heading('claude.runTitle'), code('claude', examples.shell), t('claude.runDescription'), list('claude.verify', 3, false), note('claude.loginHint'))
      break
    case 'ccswitch':
      body.push(t('ccswitch.introduction'), heading('ccswitch.autoTitle'), list('ccswitch.steps', 4), link('codex.openKey', '/keys'), note('ccswitch.importHint'), heading('ccswitch.manualTitle'), table([
        [t('field'), 'Codex', 'Claude Code'],
        [t('ccswitch.name'), '{{site_name}}', '{{site_name}}'],
        [t('ccswitch.endpoint'), '`{{openai_base_url}}`', '`{{api_root}}`'],
        ['API Key', t('ccswitch.keyValue'), t('ccswitch.keyValue')],
        [t('model'), t('ccswitch.codexModel'), t('ccswitch.claudeModel')],
        [t('ccswitch.protocol'), 'Responses', 'Anthropic Messages'],
      ]), note('ccswitch.pathWarning'), heading('ccswitch.verifyTitle'), t('ccswitch.verifyDescription'), link('viewUsage', '/usage'))
      break
    case 'chat':
      body.push(t('chat.introduction'), heading('chat.protocolTitle'), table([
        [t('ccswitch.protocol'), 'Base URL', t('chat.requestPath')],
        ['Chat Completions', '`{{openai_base_url}}`', '`/v1/chat/completions`'],
        ['Responses', '`{{openai_base_url}}`', '`/v1/responses`'],
        ['Anthropic Messages', '`{{api_root}}`', '`/v1/messages`'],
      ]), note('chat.protocolHint'), '## Cherry Studio')
      body.push([1, 2, 3, 4, 5].map(n => `${n}. ${t(`chat.cherrySteps.${n}`, { site: '{{site_name}}' })}`).join('\n'), t('chat.pathDescription'), code('{{openai_base_url}}/chat/completions'), note('chat.pathWarning'), '## Chatbox', list('chat.chatboxSteps', 3), table([
        [t('field'), t('chat.chatboxTable')], [t('chat.apiRoot'), '`{{api_root}}`'], [t('chat.apiPath'), '`/v1/chat/completions`'], ['API Key', t('ccswitch.keyValue')], [t('model'), t('ccswitch.codexModel')],
      ]), t('chat.singleUrlHint', { address: '{{openai_base_url}}' }), heading('chat.modelTitle'), t('chat.modelDescription'), note('chat.modelHint'), `[${t('chat.cherry')}](https://www.cherry-ai.com/) · [${t('chat.chatbox')}](https://chatboxai.app/)`)
      break
    case 'models':
      body.push(t('models.introduction'), heading('models.selectionTitle'), table([[t('models.useCase'), t('models.requirement')], ...[1, 2, 3, 4].map(n => [t(`models.useCases.${n}.name`), t(`models.useCases.${n}.description`)])]), note('models.selectionHint'))
      for (let n = 1; n <= 3; n++) body.push(`## ${t(`models.items.${n}.title`)}`, t(`models.items.${n}.description`))
      body.push(heading('models.cacheTitle'), t('models.cacheDescription'), heading('models.compareTitle'), list('models.compareSteps', 3), link('viewUsage', '/usage'), heading('models.modelListTitle'), t('models.modelListHint'), code(examples.models, examples.shell))
      break
    case 'billing':
      body.push(t('billing.introduction'), heading('billing.buyTitle'), list('billing.buySteps', 3), link('billing.purchase', '/purchase'), heading('billing.redeemTitle'), list('billing.redeemSteps', 3), link('billing.redeem', '/redeem'), heading('billing.walletTitle'), t('billing.walletDescription'), heading('billing.subscriptionTitle'), table([[t('billing.cardType'), t('billing.validity'), t('billing.quotaRule')], ...[1, 2, 3].map(n => [t(`billing.cards.${n}.name`), t(`billing.cards.${n}.validity`), t(`billing.cards.${n}.quota`)])]), link('billing.subscriptions', '/subscriptions'), heading('billing.priorityTitle'), t('billing.priorityDescription'), note('billing.priorityExample'), link('codex.openKey', '/keys'), heading('billing.limitsTitle'), t('billing.limitsDescription'), heading('billing.issuesTitle'))
      for (let n = 1; n <= 3; n++) body.push(`### ${t(`billing.issues.${n}.title`)}`, t(`billing.issues.${n}.description`))
      body.push(heading('billing.usageTitle'), t('billing.usageDescription'), link('viewUsage', '/usage'), note('billing.expiryHint'), heading('billing.cancelTitle'), t('billing.cancelDescription'))
      break
    case 'troubleshooting':
      body.push(t('errors.introduction'))
      for (const error of ['auth', 'balance', 'quota', 'permission', 'model', 'rate', 'upstream', 'network']) body.push(`## ${t(`errors.${error}.title`)}`, `**${t('errors.checkLabel')}**\n\n${t(`errors.${error}.check`)}`, `**${t('errors.actionLabel')}**\n\n${t(`errors.${error}.action`)}`)
      body.push(heading('errors.selfCheckTitle'), `- [ ] ${t('errors.selfCheck.site', { address: '{{site_url}}' })}\n- [ ] ${t('errors.selfCheck.api', { address: '{{api_root}}' })}\n${['key', 'provider', 'environment'].map(item => `- [ ] ${t(`errors.selfCheck.${item}`)}`).join('\n')}`, t('errors.connectionHint'), code(examples.models, examples.shell), heading('errors.supportTitle'), t('errors.supportDescription'), code(t('errors.template', { site: '{{site_url}}', api: '{{api_root}}' })), note('errors.secretWarning'), `**${t('errors.contact')}**\n\n{{contact_info}}`)
      break
  }
  return { title: t(`chapters.${id}.title`), description: t(`chapters.${id}.description`), markdown: body.join('\n\n') }
}
