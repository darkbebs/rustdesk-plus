// Comandos de instalação exibidos no dashboard.
//
// A instalação é apresentada em dois passos porque o antivírus é o suspeito
// número um quando a senha de acesso não fica gravada: o "--password" do
// cliente sai com código 0 mesmo quando a gravação do config é barrada, e o
// bloqueio não aparece em lugar nenhum. Rodar as exclusões antes, num comando
// separado, torna esse passo verificável — se com ele a senha passa a ser
// aceita, a lista abaixo é o que falta no script do instalador.

/** Pastas que o RustDesk e o instalador escrevem durante a instalação. */
const EXCLUSION_PATHS = [
  String.raw`C:\Program Files\RustDesk`,
  String.raw`C:\Program Files\RustDesk Plus`,
  // Config do usuário e — onde a senha permanente é realmente gravada — a do
  // serviço, que roda como SYSTEM (SysWOW64 é o caminho do cliente 32 bits).
  String.raw`$env:APPDATA\RustDesk`,
  String.raw`C:\Windows\System32\config\systemprofile\AppData\Roaming\RustDesk`,
  String.raw`C:\Windows\SysWOW64\config\systemprofile\AppData\Roaming\RustDesk`,
  // O .exe baixado pelo one-liner; com curinga para não excluir o TEMP inteiro.
  String.raw`$env:TEMP\rustdesk-installer-*.exe`,
];

/**
 * Passo 1 — exclusões no Windows Defender. Precisa de PowerShell como
 * administrador. Em máquina com antivírus de terceiros o cmdlet não existe: o
 * try/catch evita o erro vermelho e deixa claro que o passo foi pulado.
 */
export const defenderExclusionCmd =
  `try { Add-MpPreference -ExclusionPath ${EXCLUSION_PATHS.map((p) => `"${p}"`).join(",")} -ErrorAction Stop; ` +
  `Write-Host "Exclusoes adicionadas." -ForegroundColor Green } ` +
  `catch { Write-Host "Defender indisponivel - adicione as excecoes no seu antivirus." -ForegroundColor Yellow }`;

/**
 * Passo 2 — download e execução do instalador.
 * O prefixo de TLS 1.2 é para PowerShell antigo (Win 7/8/2012/2016), que tenta
 * TLS 1.0 e falha no HTTPS moderno. Não desabilita validação de certificado.
 */
export function installCmdFor(apiUrl: string, installCode: string): string {
  const base = apiUrl.replace(/\/$/, "");
  return `[Net.ServicePointManager]::SecurityProtocol='Tls12'; irm "${base}/i/${installCode}" | iex`;
}
