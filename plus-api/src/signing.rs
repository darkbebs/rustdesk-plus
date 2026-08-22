//! Assinatura Authenticode dos executáveis servidos, com certificado próprio
//! por tenant.
//!
//! Sem assinatura, todo .exe chega ao cliente como "editor desconhecido": o
//! SmartScreen avisa, e antivírus tratam binário sem assinatura de ferramenta
//! de acesso remoto como suspeito. Um certificado self-signed não remove o
//! aviso sozinho — a máquina precisa confiar nele —, mas com o .cer importado
//! em Root + TrustedPublisher (o script de instalação faz isso) o executável
//! passa a aparecer assinado pelo nome da empresa do tenant.
//!
//! O par é gerado com openssl e a assinatura feita com osslsigncode: o build
//! roda em Linux, onde não existe signtool/Set-AuthenticodeSignature.

use std::{
    fs,
    path::{Path, PathBuf},
    process::Command,
};
use uuid::Uuid;

/// Certificado de um tenant: .pfx para assinar, .cer para distribuir.
pub struct Cert {
    pub pfx: PathBuf,
    pub cer: PathBuf,
    /// Arquivo com a senha do .pfx — passado ao osslsigncode por -readpass,
    /// que não expõe a senha na linha de comando dos processos.
    pub pass_file: PathBuf,
    pub common_name: String,
}

fn run(command: &mut Command, description: &str) -> anyhow::Result<()> {
    let output = command.output()?;
    if output.status.success() {
        return Ok(());
    }
    anyhow::bail!(
        "{description} falhou: {}",
        String::from_utf8_lossy(&output.stderr).trim()
    )
}

pub fn signing_dir(generated_dir: &Path) -> PathBuf {
    generated_dir.join("signing")
}

/// O CN vai para dentro do subject do openssl, delimitado por "/" — barra e
/// "=" quebrariam o campo. Sobra o nome que o usuário vê no aviso do Windows.
fn sanitize_cn(name: &str) -> String {
    let cleaned: String = name
        .chars()
        .filter(|c| !matches!(c, '/' | '=' | '\\' | '"' | '\n' | '\r'))
        .collect();
    let trimmed = cleaned.trim();
    if trimmed.is_empty() {
        "RustDesk Plus".to_string()
    } else {
        trimmed.chars().take(64).collect()
    }
}

pub fn cer_path(generated_dir: &Path, tenant_id: Uuid) -> PathBuf {
    signing_dir(generated_dir).join(format!("{tenant_id}.cer"))
}

/// Devolve o certificado do tenant, gerando na primeira vez.
///
/// Trocar o nome da empresa no branding gera um certificado novo: o nome é o
/// que aparece para o cliente, e ele vive dentro do subject. As máquinas que já
/// confiavam no anterior precisam importar o novo — daí não regerar por
/// qualquer motivo, só quando o CN muda de fato.
pub fn ensure_cert(
    generated_dir: &Path,
    tenant_id: Uuid,
    common_name: &str,
) -> anyhow::Result<Cert> {
    let cn = sanitize_cn(common_name);
    let dir = signing_dir(generated_dir);
    fs::create_dir_all(&dir)?;

    let pfx = dir.join(format!("{tenant_id}.pfx"));
    let cer = dir.join(format!("{tenant_id}.cer"));
    let key = dir.join(format!("{tenant_id}.key.pem"));
    let crt = dir.join(format!("{tenant_id}.cert.pem"));
    let pass_file = dir.join(format!("{tenant_id}.pass"));
    let cn_file = dir.join(format!("{tenant_id}.cn"));

    let current_cn = fs::read_to_string(&cn_file).unwrap_or_default();
    if pfx.exists() && cer.exists() && pass_file.exists() && current_cn == cn {
        return Ok(Cert {
            pfx,
            cer,
            pass_file,
            common_name: cn,
        });
    }

    let password = Uuid::new_v4().to_string();
    fs::write(&pass_file, &password)?;
    restrict(&pass_file);

    // Certificado de code signing: end-entity, self-signed. Importado em Root
    // ele é sua própria âncora de confiança — é assim que o Windows aceita um
    // certificado sem CA.
    run(
        Command::new("openssl").args([
            "req",
            "-x509",
            "-newkey",
            "rsa:3072",
            "-sha256",
            "-days",
            "3650",
            "-nodes",
            "-keyout",
            &key.to_string_lossy(),
            "-out",
            &crt.to_string_lossy(),
            "-subj",
            &format!("/CN={cn}/O={cn}"),
            "-addext",
            "basicConstraints=critical,CA:FALSE",
            "-addext",
            "keyUsage=critical,digitalSignature",
            "-addext",
            "extendedKeyUsage=critical,codeSigning",
        ]),
        "geração do certificado",
    )?;

    run(
        Command::new("openssl").args([
            "pkcs12",
            "-export",
            "-out",
            &pfx.to_string_lossy(),
            "-inkey",
            &key.to_string_lossy(),
            "-in",
            &crt.to_string_lossy(),
            "-name",
            &cn,
            "-passout",
            &format!("pass:{password}"),
        ]),
        "empacotamento do certificado",
    )?;

    // DER: é o formato que o Import-Certificate do PowerShell espera.
    run(
        Command::new("openssl").args([
            "x509",
            "-in",
            &crt.to_string_lossy(),
            "-outform",
            "DER",
            "-out",
            &cer.to_string_lossy(),
        ]),
        "exportação do certificado público",
    )?;

    restrict(&key);
    restrict(&pfx);
    fs::write(&cn_file, &cn)?;

    Ok(Cert {
        pfx,
        cer,
        pass_file,
        common_name: cn,
    })
}

/// Chave privada e senha não devem ser legíveis por outros usuários do host —
/// quem tem a chave assina qualquer coisa em nome do tenant.
fn restrict(path: &Path) {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(path, fs::Permissions::from_mode(0o600));
    }
    #[cfg(not(unix))]
    let _ = path;
}

/// Assina o arquivo no lugar. `product` é o nome que aparece nas propriedades
/// do arquivo; `url` vai junto como site do publisher.
pub fn sign(cert: &Cert, path: &Path, product: &str, url: &str) -> anyhow::Result<()> {
    let signed = path.with_extension("signing.tmp");
    let mut base: Vec<String> = vec![
        "sign".into(),
        "-pkcs12".into(),
        cert.pfx.to_string_lossy().into(),
        "-readpass".into(),
        cert.pass_file.to_string_lossy().into(),
        "-h".into(),
        "sha256".into(),
        "-n".into(),
        product.to_string(),
    ];
    if !url.is_empty() {
        base.push("-i".into());
        base.push(url.to_string());
    }
    base.push("-in".into());
    base.push(path.to_string_lossy().into());
    base.push("-out".into());
    base.push(signed.to_string_lossy().into());

    // Com carimbo de tempo a assinatura continua válida depois de o certificado
    // expirar. Depende de rede: se o servidor de timestamp não responder, é
    // melhor assinar sem do que servir um .exe sem assinatura nenhuma.
    let mut with_ts = base.clone();
    with_ts.push("-ts".into());
    with_ts.push("http://timestamp.digicert.com".into());

    let mut result = run(Command::new("osslsigncode").args(&with_ts), "assinatura");
    if result.is_err() {
        let _ = fs::remove_file(&signed);
        tracing::warn!("assinatura sem carimbo de tempo (servidor de timestamp indisponível)");
        result = run(Command::new("osslsigncode").args(&base), "assinatura");
    }
    if let Err(err) = result {
        let _ = fs::remove_file(&signed);
        return Err(err);
    }

    fs::rename(&signed, path).or_else(|_| {
        fs::copy(&signed, path)?;
        fs::remove_file(&signed)
    })?;
    Ok(())
}

/// Assina só se ainda não estiver assinado com este certificado.
///
/// O sidecar guarda tamanho e mtime do arquivo assinado: um build novo muda os
/// dois, e aí a assinatura é refeita. Serve para o cliente com marca, que é
/// gerado fora daqui e só passa por este caminho ao ser baixado.
pub fn sign_if_needed(cert: &Cert, path: &Path, product: &str, url: &str) -> anyhow::Result<()> {
    let marker = PathBuf::from(format!("{}.signed", path.to_string_lossy()));
    let stamp = file_stamp(path, &cert.common_name)?;
    if fs::read_to_string(&marker).map(|s| s == stamp).unwrap_or(false) {
        return Ok(());
    }
    sign(cert, path, product, url)?;
    let _ = fs::write(&marker, file_stamp(path, &cert.common_name)?);
    Ok(())
}

fn file_stamp(path: &Path, cn: &str) -> anyhow::Result<String> {
    let meta = fs::metadata(path)?;
    let modified = meta
        .modified()
        .ok()
        .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
        .map(|d| d.as_secs())
        .unwrap_or(0);
    Ok(format!("{}:{}:{}", meta.len(), modified, cn))
}
