// Comandos de instalação exibidos no dashboard.
//
// Cada passo vai num comando separado, e não numa linha só com `;`: assim um
// que falhe (antivírus de terceiros sem Add-MpPreference, PowerShell que recusa
// mexer no ServicePointManager) não leva a instalação junto, e dá para ver qual
// foi.
//
// A instalação é apresentada em passos porque o antivírus é o suspeito
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
 * Passo 2 — TLS 1.2.
 *
 * Serve para PowerShell antigo (Win 7/8/2012/2016), que tenta TLS 1.0 e falha
 * no HTTPS moderno. Não desabilita validação de certificado.
 *
 * Vai num comando próprio, e não grudado no `irm`, porque nem toda máquina
 * aceita essa atribuição: no PowerShell 7 o ServicePointManager está obsoleto e
 * em política restrita a linha pode ser recusada. Colado antes do `irm` com
 * `;`, o erro derrubava a instalação junto; sozinho e em try/catch, falha só
 * ele — quem já negocia TLS 1.2 por padrão (Windows 10/11 atualizado) não
 * precisa dele de qualquer forma.
 *
 * O enum em vez da string 'Tls12': a conversão implícita é o que falha em parte
 * dos casos.
 */
export const tlsCmd =
  `try { [Net.ServicePointManager]::SecurityProtocol = ` +
  `[Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12; ` +
  `Write-Host "TLS 1.2 habilitado." -ForegroundColor Green } ` +
  `catch { Write-Host "TLS 1.2 ja e o padrao nesta versao do Windows." -ForegroundColor Yellow }`;

/**
 * Passo 3 — confiar no certificado que assina os executáveis.
 *
 * Os .exe são assinados com um certificado próprio do tenant (o nome da empresa
 * aparece no aviso do Windows). Um certificado self-signed não vale nada até a
 * máquina confiar nele: importado em Root ele vira sua própria âncora, e em
 * TrustedPublisher o Windows para de tratar o editor como desconhecido.
 *
 * Isso também roda dentro do script automático; o comando avulso é para quem
 * instala pelo .exe baixado à mão.
 *
 * certutil como alternativa ao Import-Certificate: este vem do módulo PKI, que
 * não existe no PowerShell 2.0 do Windows 7 original.
 */
export function trustCertCmdFor(apiUrl: string, installCode: string): string {
  const base = apiUrl.replace(/\/$/, "");
  return (
    `$c = "$env:TEMP\\rustdesk-plus.cer"; ` +
    `irm "${base}/cert/${installCode}" -OutFile $c; ` +
    `foreach ($s in @("Root","TrustedPublisher")) { ` +
    `try { Import-Certificate -FilePath $c -CertStoreLocation "Cert:\\LocalMachine\\$s" | Out-Null } ` +
    `catch { certutil.exe -addstore -f $s $c | Out-Null } }; ` +
    `Write-Host "Certificado instalado." -ForegroundColor Green`
  );
}

/** Passo 4 — download e execução do instalador. */
export function installCmdFor(apiUrl: string, installCode: string): string {
  const base = apiUrl.replace(/\/$/, "");
  return `irm "${base}/i/${installCode}" | iex`;
}
