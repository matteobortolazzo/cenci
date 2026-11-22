#!/usr/bin/env perl
# Check personal skills' native frontmatter names before publishing any links.
# CPAN::Meta::YAML ships with Perl. Unreadable or unsupported metadata stops
# activation rather than silently shadowing a local skill.
use strict;
use warnings;
use CPAN::Meta::YAML;
use File::Basename qw(basename dirname);

my ($stage, $skills, $legacy) = @ARGV;
my %selected;

sub skill_name {
    my ($manifest) = @_;
    open my $fh, '<', $manifest or die "Host content: cannot read $manifest: $!\n";
    my $directory_name = basename(dirname($manifest));
    my $first = <$fh>;
    return $directory_name unless defined $first && $first =~ /^---\s*$/;
    my $header = "---\n";
    while (my $line = <$fh>) {
        if ($line =~ /^(?:---|\.\.\.)\s*$/) {
            my $doc = eval { CPAN::Meta::YAML->read_string($header) };
            die "Host content: cannot parse skill frontmatter in $manifest\n"
                unless $doc && ref($doc->[0]) eq 'HASH';
            my $name = $doc->[0]{name} // $directory_name;
            die "Host content: invalid skill name in $manifest\n" if ref($name) || !length($name);
            return $name;
        }
        $header .= $line;
    }
    die "Host content: unterminated skill frontmatter in $manifest\n";
}

sub children {
    my ($dir) = @_;
    opendir my $dh, $dir or die "Host content: cannot inspect $dir: $!\n";
    return map { "$dir/$_" } grep { substr($_,0,1) ne '.' } readdir $dh;
}

exit 0 unless -d "$stage/skills";
for my $source (children("$stage/skills")) {
    my $name = skill_name("$source/SKILL.md");
    die "Host content: selected skills $source and $selected{$name} both declare $name; deselect one.\n"
        if exists $selected{$name};
    $selected{$name} = $source;
}

for my $dir ($skills, $legacy) {
    next unless defined $dir && -d $dir;
    my @dirs = children($dir);
    my %seen;
    while (my $local = pop @dirs) {
        next unless -d $local;
        # Our discovery link is the selected source itself.
        next if dirname($local) eq $skills && -l $local
            && readlink($local) eq "$stage/skills/" . basename($local);
        my ($dev,$ino) = stat $local;
        next if $seen{"$dev:$ino"}++;
        if (-f "$local/SKILL.md") {
            my $name = skill_name("$local/SKILL.md");
            die "Host content: $local and $selected{$name} both declare skill $name; rename it or deselect the host skill.\n"
                if exists $selected{$name};
        } else {
            push @dirs, grep { -d $_ } children($local);
        }
    }
}

